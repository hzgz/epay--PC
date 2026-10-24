package main

import (
	"fmt"
	"os"
	"testing"
)

func TestParseSaved(t *testing.T) {
	files := []string{
		`C:\Users\Administrator\Desktop\pcjk\wxhook\msg_0_771.bin`,
		`C:\Users\Administrator\Desktop\pcjk\wxhook\msg_1_746.bin`,
	}
	for _, fn := range files {
		d, err := os.ReadFile(fn)
		if err != nil {
			t.Logf("%s 读取失败: %v", fn, err)
			continue
		}
		fmt.Printf("\n===== %s (%d 字节) =====\n", fn, len(d))

		var fs []PBField
		ParsePB(d, 0, "", &fs)
		fmt.Printf("ParsePB 字段数: %d\n", len(fs))
		for _, f := range fs {
			if f.Wire == 2 && len(f.Bytes) > 1 && len(f.Bytes) < 200 {
				if s := trimStr(f.Bytes); s != "" {
					fmt.Printf("  %s = %q\n", f.Path, s)
				}
			}
		}

		m := ExtractMsg(d)
		if m == nil {
			fmt.Println("ExtractMsg => nil")
		} else {
			fmt.Printf("ExtractMsg => From=%q To=%q Content=%q Display=%q MsgID=%d Time=%d\n",
				m.From, m.To, m.Content, m.Display, m.MsgID, m.Time)
		}
	}
}
