package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc64"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	indexMagic        = 0x494E4458
	indexVersion      = 2
	indexHeaderFormat = 1
)

var crc64Table = crc64.MakeTable(crc64.ECMA)

type indexHeader struct {
	Magic      uint32
	IndexType  uint32
	Version    uint64
	RootOffset uint64
	NodeCount  uint64
	PageSize   uint32
	Format     uint32
	EntryCount uint64
}

func main() {
	indexDir := flag.String("dir", "./data/indexes", "索引目录路径")
	fix := flag.Bool("fix", false, "尝试修复损坏的索引文件（将损坏文件重命名为.bak）")
	verbose := flag.Bool("v", false, "详细输出")
	flag.Parse()

	fmt.Printf("索引诊断工具\n")
	fmt.Printf("============================================\n")
	fmt.Printf("扫描目录: %s\n\n", *indexDir)

	if _, err := os.Stat(*indexDir); os.IsNotExist(err) {
		fmt.Printf("错误: 索引目录不存在: %s\n", *indexDir)
		fmt.Printf("建议: 首次运行程序会自动创建索引\n")
		os.Exit(1)
	}

	hasIssues := false
	fixCount := 0

	// 扫描索引目录
	err := filepath.WalkDir(*indexDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		// 只检查 .idx 文件
		if !strings.HasSuffix(path, ".idx") {
			return nil
		}

		fmt.Printf("\n检查文件: %s\n", path)
		fmt.Printf("--------------------------------------------\n")

		valid, issues := validateIndexFile(path, *verbose)
		if !valid {
			hasIssues = true
			fmt.Printf("状态: ❌ 发现问题\n")
			for _, issue := range issues {
				fmt.Printf("  - %s\n", issue)
			}

			if *fix {
				backupPath := path + ".bak"
				if err := os.Rename(path, backupPath); err != nil {
					fmt.Printf("  修复失败: 无法重命名文件: %v\n", err)
				} else {
					fmt.Printf("  已将损坏文件重命名为: %s\n", backupPath)
					fixCount++
				}
			} else {
				fmt.Printf("  建议: 使用 -fix 参数自动修复\n")
			}
		} else {
			fmt.Printf("状态: ✅ 正常\n")
		}

		return nil
	})

	if err != nil {
		fmt.Printf("\n错误: 扫描目录失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n============================================\n")
	fmt.Printf("诊断完成\n")
	if hasIssues {
		fmt.Printf("发现问题的文件需要处理\n")
		if fixCount > 0 {
			fmt.Printf("已修复 %d 个文件\n", fixCount)
			fmt.Printf("\n下次启动程序时，系统会自动重建索引\n")
		} else if !*fix {
			fmt.Printf("运行 `index-doctor -dir %s -fix` 来修复问题\n", *indexDir)
		}
	} else {
		fmt.Printf("所有索引文件正常\n")
	}
}

func validateIndexFile(path string, verbose bool) (bool, []string) {
	var issues []string

	fi, err := os.Stat(path)
	if err != nil {
		issues = append(issues, fmt.Sprintf("无法读取文件信息: %v", err))
		return false, issues
	}

	fileSize := fi.Size()
	if verbose {
		fmt.Printf("文件大小: %d 字节\n", fileSize)
	}

	// 检查文件是否为空
	if fileSize == 0 {
		issues = append(issues, "文件为空")
		return false, issues
	}

	// 尝试读取并验证 header
	file, err := os.Open(path)
	if err != nil {
		issues = append(issues, fmt.Sprintf("无法打开文件: %v", err))
		return false, issues
	}
	defer file.Close()

	// 检测页面大小（尝试常见的页面大小）
	pageSizes := []uint32{4096, 8192, 16384}
	var header indexHeader
	var pageSize uint32
	headerValid := false

	for _, ps := range pageSizes {
		if int64(ps) > fileSize {
			continue
		}

		buf := make([]byte, ps)
		if _, err := file.ReadAt(buf, 0); err != nil {
			continue
		}

		// 验证 magic
		magic := binary.BigEndian.Uint32(buf[0:4])
		if magic != indexMagic {
			continue
		}

		// 验证 checksum
		stored := binary.BigEndian.Uint64(buf[int(ps)-8:])
		calc := crc64.Checksum(buf[:int(ps)-8], crc64Table)
		if stored != calc {
			continue
		}

		// Header 有效
		header.Magic = magic
		header.IndexType = binary.BigEndian.Uint32(buf[4:8])
		header.Version = binary.BigEndian.Uint64(buf[8:16])
		header.RootOffset = binary.BigEndian.Uint64(buf[16:24])
		header.NodeCount = binary.BigEndian.Uint64(buf[24:32])
		header.PageSize = binary.BigEndian.Uint32(buf[32:36])
		header.Format = binary.BigEndian.Uint32(buf[36:40])
		header.EntryCount = binary.BigEndian.Uint64(buf[40:48])
		pageSize = ps
		headerValid = true
		break
	}

	if !headerValid {
		issues = append(issues, "Header 损坏或格式不正确")
		return false, issues
	}

	if verbose {
		fmt.Printf("页面大小: %d\n", pageSize)
		fmt.Printf("索引类型: %d\n", header.IndexType)
		fmt.Printf("版本: %d\n", header.Version)
		fmt.Printf("根节点偏移: %d\n", header.RootOffset)
		fmt.Printf("节点数量: %d\n", header.NodeCount)
		fmt.Printf("条目数量: %d\n", header.EntryCount)
	}

	// 验证版本
	if header.Version != indexVersion {
		issues = append(issues, fmt.Sprintf("索引版本不匹配: 期望 %d, 实际 %d (需要重建)", indexVersion, header.Version))
		return false, issues
	}

	// 验证页面大小
	if header.PageSize != pageSize {
		issues = append(issues, fmt.Sprintf("页面大小不匹配: header 中为 %d, 实际为 %d", header.PageSize, pageSize))
		return false, issues
	}

	// 检查根节点偏移是否在文件范围内
	if header.RootOffset > 0 {
		if int64(header.RootOffset) >= fileSize {
			issues = append(issues, fmt.Sprintf("根节点偏移 %d 超出文件大小 %d", header.RootOffset, fileSize))
			return false, issues
		}

		if int64(header.RootOffset)+int64(pageSize) > fileSize {
			issues = append(issues, fmt.Sprintf("根节点页面(偏移=%d, 大小=%d)超出文件大小 %d",
				header.RootOffset, pageSize, fileSize))
			return false, issues
		}
	}

	// 检查节点数量是否合理
	expectedFileSize := int64(pageSize) + int64(header.NodeCount)*int64(pageSize)
	if fileSize < expectedFileSize {
		issues = append(issues, fmt.Sprintf("文件大小 %d 小于预期大小 %d (基于节点数 %d)",
			fileSize, expectedFileSize, header.NodeCount))
		return false, issues
	}

	return true, nil
}
