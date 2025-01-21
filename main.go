package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liyue201/goqr"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	// 1つのQRコードに格納するBase64文字列の最大長。
	// 実際にはエラー訂正レベルなどによって格納可能なデータ量が変わるため、
	// 余裕をもって小さめにしている。
	chunkSize = 1200
)

// usage : 引数の説明を表示する
func usage() {
	fmt.Println("Usage:")
	fmt.Println("  main encode <inputFile> <outputDir>")
	fmt.Println("    -> 指定したファイルをQRコード(複数PNG)に分割して出力します。")
	fmt.Println()
	fmt.Println("  main decode <inputDir> <outputFile>")
	fmt.Println("    -> 指定したディレクトリにあるPNGファイルをすべて読み込みQRコードを解析し、")
	fmt.Println("       復元したバイナリを指定ファイルに書き出します。")
}

// encodeFile : ファイルを読み込み、Base64エンコードして複数のQRコードに変換
func encodeFile(inputFile, outputDir string) error {
	// ファイル読み込み
	data, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("ファイル読み込み失敗: %w", err)
	}

	// Base64エンコード
	encoded := base64.StdEncoding.EncodeToString(data)

	// チャンク分割
	chunks := splitIntoChunks(encoded, chunkSize)
	totalChunks := len(chunks)

	// 出力先ディレクトリが無い場合は作成
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("出力ディレクトリ作成失敗: %w", err)
	}

	// 各チャンクをQRコードに変換してPNG保存
	for i, chunk := range chunks {
		// 格納する文字列の形式:
		// "index/totalChunks:chunkData"
		content := fmt.Sprintf("%d/%d:%s", i, totalChunks, chunk)

		// QRコード生成 (エラー訂正レベルなどは任意に設定可能)
		pngData, err := qrcode.Encode(content, qrcode.Medium, 256)
		if err != nil {
			return fmt.Errorf("QRコード生成失敗: %w", err)
		}

		// ファイル名は "qr_<連番>.png" のようにする
		outputFilePath := filepath.Join(outputDir, fmt.Sprintf("qr_%05d.png", i))
		if err := os.WriteFile(outputFilePath, pngData, 0644); err != nil {
			return fmt.Errorf("QRコード書き込み失敗: %w", err)
		}
	}

	fmt.Printf("合計 %d 個のQRコードを生成しました。出力ディレクトリ: %s\n", totalChunks, outputDir)
	return nil
}

// decodeQRCodes : ディレクトリ内のQRコード(PNG)を解析してBase64文字列を再構成
func decodeQRCodes(inputDir string) (string, error) {
	// ディレクトリ内のPNGファイル一覧を取得
	files, err := os.ReadDir(inputDir)
	if err != nil {
		return "", fmt.Errorf("ディレクトリ読み込み失敗: %w", err)
	}

	// チャンクを格納するためのマップ (index -> chunkData)
	chunksMap := make(map[int]string)
	totalChunks := -1

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		// 拡張子が .png でなければスキップ
		if filepath.Ext(f.Name()) != ".png" {
			continue
		}

		// ファイルパスを取得
		path := filepath.Join(inputDir, f.Name())
		fileData, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("ファイル読み込み失敗(%s): %w", path, err)
		}

		// []byte を image.Image にデコード
		img, _, err := image.Decode(bytes.NewReader(fileData))
		if err != nil {
			return "", fmt.Errorf("画像デコード失敗(%s): %w", path, err)
		}

		// QRコード解析
		qrCodes, err := goqr.Recognize(img)
		if err != nil {
			return "", fmt.Errorf("QRコード解析失敗(%s): %w", path, err)
		}

		// 通常は1枚のQRコードデータが含まれている想定(複数含まれる場合もあるため考慮)
		for _, qr := range qrCodes {
			text := string(qr.Payload)
			// "index/total:chunkData" の形式を想定
			parts := strings.SplitN(text, ":", 2)
			if len(parts) != 2 {
				log.Printf("予期しないQRコードデータ形式: %s\n", text)
				continue
			}

			meta := parts[0]      // "index/total"
			chunkData := parts[1] // base64チャンク

			metaParts := strings.SplitN(meta, "/", 2)
			if len(metaParts) != 2 {
				log.Printf("メタ情報の形式が不正: %s\n", meta)
				continue
			}

			indexStr := metaParts[0]
			totalStr := metaParts[1]

			idx, err := strconv.Atoi(indexStr)
			if err != nil {
				log.Printf("indexが数値でない: %s\n", indexStr)
				continue
			}

			tChunks, err := strconv.Atoi(totalStr)
			if err != nil {
				log.Printf("totalChunksが数値でない: %s\n", totalStr)
				continue
			}

			// 最初に見つかった totalChunks が正と仮定し、他のQRで異なる値があれば警告
			if totalChunks == -1 {
				totalChunks = tChunks
			} else if totalChunks != tChunks {
				log.Printf("想定しているチャンク総数(%d)と異なる値(%d)を検出\n", totalChunks, tChunks)
			}

			chunksMap[idx] = chunkData
		}
	}

	if totalChunks <= 0 {
		return "", fmt.Errorf("QRコードから総チャンク数が取得できませんでした")
	}

	// 0 から totalChunks-1 まで順番に再連結
	builder := strings.Builder{}
	for i := 0; i < totalChunks; i++ {
		chunk, ok := chunksMap[i]
		if !ok {
			return "", fmt.Errorf("チャンク %d が見つかりません", i)
		}
		builder.WriteString(chunk)
	}

	return builder.String(), nil
}

// decodeFile : QRコード群からファイルを復元
func decodeFile(inputDir, outputFile string) error {
	// ディレクトリ内のQRコードを解析してBase64文字列を構築
	base64Data, err := decodeQRCodes(inputDir)
	if err != nil {
		return err
	}

	// Base64文字列をデコードしてバイナリに戻す
	decoded, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("Base64デコード失敗: %w", err)
	}

	// ファイルに書き出し
	if err := os.WriteFile(outputFile, decoded, 0644); err != nil {
		return fmt.Errorf("ファイル書き込み失敗: %w", err)
	}

	fmt.Printf("ファイルを復元しました: %s\n", outputFile)
	return nil
}

// splitIntoChunks : 文字列を指定したサイズに分割する
func splitIntoChunks(s string, size int) []string {
	var chunks []string
	for len(s) > size {
		chunks = append(chunks, s[:size])
		s = s[size:]
	}
	chunks = append(chunks, s)
	return chunks
}

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}

	command := os.Args[1]

	switch command {
	case "encode":
		if len(os.Args) != 4 {
			fmt.Println("引数が足りません。")
			usage()
			return
		}
		inputFile := os.Args[2]
		outputDir := os.Args[3]
		if err := encodeFile(inputFile, outputDir); err != nil {
			log.Fatalf("encode失敗: %v", err)
		}

	case "decode":
		if len(os.Args) != 4 {
			fmt.Println("引数が足りません。")
			usage()
			return
		}
		inputDir := os.Args[2]
		outputFile := os.Args[3]
		if err := decodeFile(inputDir, outputFile); err != nil {
			log.Fatalf("decode失敗: %v", err)
		}

	default:
		usage()
	}
}
