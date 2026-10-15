package ipmsg

import (
	"fmt"
	"strconv"
	"strings"
)

// FileEntry 表示一条附件通告中的文件（UDP SENDMSG|FILEATTACH 携带）。
// Name 始终保存 UTF-8，编码转换发生在 EncodeAttach/DecodeAttach 边界。
type FileEntry struct {
	ID    uint32
	Name  string
	Size  int64
	Mtime int64
	Attr  uint32 // FileRegular / FileDir
}

// EncodeAttach 把文件列表编码为附件通告串：
// "id:name:size(hex):mtime(hex):attr(hex)[:ext=val]"，
// 条目之间以 ":\a:" 分隔（iptux 靠 '\a' 定位下一条目；
// 飞秋按冒号切分，把 "\a" 当多余扩展段忽略，两边都兼容），
// 文件名中的 ':' 按 "::" 转义。
func EncodeAttach(files []FileEntry, gbk bool) string {
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString(":\a:")
		}
		name := string(EncodeTextOut(f.Name, gbk))
		name = strings.ReplaceAll(name, ":", "::")
		fmt.Fprintf(&b, "%d:%s:%x:%x:%x", f.ID, name, f.Size, f.Mtime, f.Attr)
	}
	return b.String()
}

// DecodeAttach 解析附件通告串。兼容 "::" 转义与 extend 属性段。
func DecodeAttach(s string) ([]FileEntry, error) {
	var files []FileEntry
	pos := 0
	for pos < len(s) {
		var f FileEntry
		var raw string
		var err error

		if raw, pos, err = nextField(s, pos); err != nil {
			return nil, err
		}
		if f.ID, err = parseUint(raw, 10); err != nil {
			return nil, fmt.Errorf("文件ID解析失败: %w", err)
		}
		if raw, pos, err = nextField(s, pos); err != nil {
			return nil, err
		}
		f.Name = DecodeText([]byte(unescapeName(raw)))
		if raw, pos, err = nextField(s, pos); err != nil {
			return nil, err
		}
		if f.Size, err = parseInt(raw, 16); err != nil {
			return nil, fmt.Errorf("文件大小解析失败: %w", err)
		}
		if raw, pos, err = nextField(s, pos); err != nil {
			return nil, err
		}
		if f.Mtime, err = parseInt(raw, 16); err != nil {
			return nil, fmt.Errorf("文件时间解析失败: %w", err)
		}
		if raw, pos, err = nextField(s, pos); err != nil {
			return nil, err
		}
		if f.Attr, err = parseUint(raw, 16); err != nil {
			return nil, fmt.Errorf("文件属性解析失败: %w", err)
		}
		// 跳过 extend 属性段（"41=xxx"）与 iptux 的条目分隔 "\a"
		for pos < len(s) {
			save := pos
			ext, np, err := nextField(s, pos)
			if err != nil || (ext != "\a" && !strings.Contains(ext, "=")) {
				pos = save
				break
			}
			pos = np
		}
		files = append(files, f)
		// 残余串再无完整条目（非数字开头）时容错结束，不整串报错
		if pos < len(s) {
			if next, _, err := nextField(s, pos); err != nil {
				return files, nil
			} else if _, perr := parseUint(next, 10); perr != nil {
				return files, nil
			}
		}
	}
	return files, nil
}

// nextField 读取一个 ':' 分隔的字段，处理 "::" 转义。
// 返回字段内容与下一个读取位置；err 非空表示字段不足。
func nextField(s string, pos int) (string, int, error) {
	if pos >= len(s) {
		return "", pos, fmt.Errorf("通告串字段不足: %q", s)
	}
	var b strings.Builder
	i := pos
	for i < len(s) {
		c := s[i]
		if c == ':' {
			if i+1 < len(s) && s[i+1] == ':' { // 转义的冒号
				b.WriteByte(':')
				i += 2
				continue
			}
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	// 末尾字段（无冒号终止）
	return b.String(), len(s), nil
}

func unescapeName(s string) string {
	return strings.ReplaceAll(s, "::", ":")
}

func parseUint(s string, base int) (uint32, error) {
	v, err := strconv.ParseUint(s, base, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

func parseInt(s string, base int) (int64, error) {
	return strconv.ParseInt(s, base, 64)
}
