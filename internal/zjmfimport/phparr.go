package zjmfimport

import (
	"errors"
	"strings"
)

// 本文件实现两件小事：
//  1. stripComments —— 把 PHP 源码里的注释抹成空格（保留行号与偏移），
//     避免被注释掉的旧代码干扰正则提取（nokvm 里就有大段注释函数）。
//  2. 一个最小 PHP 数组字面量解析器，专用于 _MetaData / _ConfigOptions
//     的返回数组 —— 只支持单双引号字符串、数字、true/false/null、
//     嵌套数组（[...] 与 array(...)）与 key => value，这正是五个随包
//     模块用到的全部语法。

type phpValue struct {
	str   string
	isStr bool
	list  []phpValue
	dict  map[string]phpValue
	keys  []string // dict 的插入顺序
}

func (v phpValue) asString() string { return v.str }

func (v phpValue) dictGet(key string) phpValue {
	if v.dict == nil {
		return phpValue{}
	}
	return v.dict[key]
}

// stripComments 把 //、#、/* */ 注释替换为等长空白（保留换行），字符串内的
// 内容原样保留。
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			quote := c
			b.WriteByte(c)
			i++
			for i < len(src) {
				if src[i] == '\\' && i+1 < len(src) {
					b.WriteByte(src[i])
					b.WriteByte(src[i+1])
					i += 2
					continue
				}
				b.WriteByte(src[i])
				if src[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
		case c == '#':
			i = skipLineComment(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				for ; i < len(src); i++ {
					if src[i] == '\n' {
						b.WriteByte('\n')
					} else {
						b.WriteByte(' ')
					}
				}
			} else {
				for j := 0; j < end+4; j++ {
					if src[i+j] == '\n' {
						b.WriteByte('\n')
					} else {
						b.WriteByte(' ')
					}
				}
				i += end + 4
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func skipLineComment(src string, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	return i
}

// parsePHPArray 解析 src 中从 offset（应指向 '[' 或 'array(' 的 '('）开始的
// 一个数组字面量，返回值与结束符之后的偏移。
func parsePHPArray(src string, offset int) (phpValue, int, error) {
	if offset >= len(src) {
		return phpValue{}, 0, errors.New("PHP 数组解析：意外结束")
	}
	var closer byte
	switch src[offset] {
	case '[':
		closer = ']'
	case '(':
		closer = ')'
	default:
		return phpValue{}, 0, errors.New("PHP 数组解析：起点不是数组")
	}
	out := phpValue{dict: map[string]phpValue{}}
	i := offset + 1
	for {
		i = skipSpace(src, i)
		if i >= len(src) {
			return phpValue{}, 0, errors.New("PHP 数组解析：未闭合")
		}
		if src[i] == closer {
			return out, i + 1, nil
		}
		val, next, err := parsePHPScalar(src, i, closer)
		if err != nil {
			return phpValue{}, 0, err
		}
		i = skipSpace(src, next)
		if i+1 < len(src) && src[i] == '=' && src[i+1] == '>' {
			// key => value
			key := val.asString()
			i = skipSpace(src, i+2)
			v2, next2, err := parsePHPScalar(src, i, closer)
			if err != nil {
				return phpValue{}, 0, err
			}
			if _, dup := out.dict[key]; !dup {
				out.keys = append(out.keys, key)
			}
			out.dict[key] = v2
			i = skipSpace(src, next2)
		} else {
			out.list = append(out.list, val)
		}
		if i < len(src) && src[i] == ',' {
			i++
			continue
		}
	}
}

// parsePHPScalar 解析一个标量或嵌套数组。
func parsePHPScalar(src string, i int, closer byte) (phpValue, int, error) {
	i = skipSpace(src, i)
	if i >= len(src) {
		return phpValue{}, 0, errors.New("PHP 数组解析：意外结束")
	}
	switch {
	case src[i] == '[':
		return parsePHPArray(src, i)
	case src[i] == '\'' || src[i] == '"':
		s, next, err := parsePHPString(src, i)
		return phpValue{str: s, isStr: true}, next, err
	case src[i] == '-' || (src[i] >= '0' && src[i] <= '9'):
		j := i + 1
		for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
			j++
		}
		return phpValue{str: src[i:j]}, j, nil
	default:
		// PHP 变量/表达式值（如 $a、$params['x']['y']、$a . $b）：整段消费到
		// 顶层分隔符（',' 或数组结束符），字符串片段仍按字符串读取。
		if src[i] == '$' || src[i] == '.' || src[i] == '+' || src[i] == '-' && !(i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9') {
			p, val, err := consumeRawExpr(src, i, closer)
			if err != nil {
				return phpValue{}, 0, err
			}
			return phpValue{str: val}, p, nil
		}
		j := i
		for j < len(src) && isWordByte(src[j]) {
			j++
		}
		word := src[i:j]
		switch strings.ToLower(word) {
		case "true":
			return phpValue{str: "1"}, j, nil
		case "false", "null":
			return phpValue{str: ""}, j, nil
		case "":
			return phpValue{}, 0, errors.New("PHP 数组解析：无法识别的值 " + safeSnippet(src, i))
		}
		// array( 嵌套写法
		if strings.EqualFold(word, "array") {
			p := skipSpace(src, j)
			if p < len(src) && src[p] == '(' {
				return parsePHPArray(src, p)
			}
		}
		// 其它函数调用/表达式（如 randStr(6)）：整段原样吞掉，取词作值。
		p := skipSpace(src, j)
		if p < len(src) && src[p] == '(' {
			depth := 0
			for p < len(src) {
				switch src[p] {
				case '(':
					depth++
				case ')':
					depth--
				}
				p++
				if depth == 0 {
					break
				}
			}
			return phpValue{str: word}, p, nil
		}
		return phpValue{str: word}, j, nil
	}
}

// consumeRawExpr 消费一个原始 PHP 表达式（到顶层 ',' 或 close 为止），
// 返回结束偏移与原文（用于调试），字符串片段按引号规则跳过。
func consumeRawExpr(src string, i int, close byte) (int, string, error) {
	start := i
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			q := c
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case c == '(':
			depth := 0
			for i < len(src) {
				switch src[i] {
				case '(':
					depth++
				case ')':
					depth--
				}
				i++
				if depth == 0 {
					break
				}
			}
		case c == ',' || c == close:
			return i, src[start:i], nil
		case c == ')' || c == ']' || c == ';':
			return i, src[start:i], nil
		default:
			i++
		}
	}
	return 0, "", errors.New("PHP 数组解析：表达式未终止")
}

func parsePHPString(src string, i int) (string, int, error) {
	quote := src[i]
	var b strings.Builder
	i++
	for i < len(src) {
		c := src[i]
		if c == '\\' && i+1 < len(src) {
			next := src[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default: // \' \" \\ \$ 等
				b.WriteByte(next)
			}
			i += 2
			continue
		}
		if c == quote {
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return "", 0, errors.New("PHP 数组解析：字符串未闭合")
}

func skipSpace(src string, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func safeSnippet(src string, i int) string {
	end := i + 40
	if end > len(src) {
		end = len(src)
	}
	return strings.TrimSpace(src[i:end])
}

// findReturnArray 在函数体里找第一个 return 后跟的数组字面量并解析，
// 同时支持 [...] 与 array(...) 两种写法。
func findReturnArray(body string) (phpValue, bool) {
	idx := 0
	for {
		pos := strings.Index(body[idx:], "return")
		if pos < 0 {
			return phpValue{}, false
		}
		pos += idx
		// return 必须是独立词
		if pos > 0 && isWordByte(body[pos-1]) {
			idx = pos + 6
			continue
		}
		after := pos + 6
		if after < len(body) && isWordByte(body[after]) {
			idx = pos + 6
			continue
		}
		br := skipSpace(body, after)
		if br < len(body) && body[br] == '[' {
			v, _, err := parsePHPArray(body, br)
			if err != nil {
				return phpValue{}, false
			}
			return v, true
		}
		if br+6 <= len(body) && strings.EqualFold(body[br:br+5], "array") {
			p := skipSpace(body, br+5)
			if p < len(body) && body[p] == '(' {
				v, _, err := parsePHPArray(body, p)
				if err != nil {
					return phpValue{}, false
				}
				return v, true
			}
		}
		idx = pos + 6
	}
}
