package executor

import "testing"

func TestWindowsOutputEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"UTF8 Chinese", []byte("新增卷\r\n"), "新增卷\r\n"},
		{"GBK Chinese", []byte{0xd6, 0xd0, 0xce, 0xc4}, "中文"},
		{"ASCII", []byte("ERROR: Invalid syntax."), "ERROR: Invalid syntax."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeWindowsOutput(tc.raw); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
