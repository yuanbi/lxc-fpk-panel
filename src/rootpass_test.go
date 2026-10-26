package main

import (
	"strings"
	"testing"
)

func TestRandomPasswordShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p := randomPassword(defaultPassLen)
		if len(p) != defaultPassLen {
			t.Fatalf("长度应为 %d，实际 %d（%q）", defaultPassLen, len(p), p)
		}
		if strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("密码不应包含易混淆字符: %q", p)
		}
		for _, r := range p {
			if !strings.ContainsRune(passAlphabet, r) {
				t.Fatalf("出现非法字符 %q in %q", r, p)
			}
		}
		seen[p] = true
	}
	if len(seen) < 195 {
		t.Fatalf("随机性不足，200 次只生成 %d 个不同密码", len(seen))
	}
	if got := len(randomPassword(0)); got != defaultPassLen {
		t.Fatalf("传 0 时应回落到默认长度，实际 %d", got)
	}
}

func TestValidPassword(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"abcde", false},        // 太短
		{"abcdef", true},        // 刚好 6 位
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"abc\ndef", false},
		{"abc\rdef", false},
	}
	for _, c := range cases {
		err := validPassword(c.in)
		if c.ok && err != nil {
			t.Fatalf("%q 应通过，却报错 %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%q 应被拒绝", c.in)
		}
	}
}
