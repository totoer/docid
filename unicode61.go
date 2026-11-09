package docid

import "sort"

func unicode61Category(code rune) int {
	if code < 0 || code >= 1<<20 {
		return 0
	}
	lo, hi := int(unicodeCategoryBlocks[code>>16]), int(unicodeCategoryBlocks[1+(code>>16)])
	key := uint16(code)
	i := lo + sort.Search(hi-lo, func(i int) bool { return unicodeCategoryMap[lo+i] > key }) - 1
	if i < lo || int(key) >= int(unicodeCategoryMap[i])+int(unicodeCategoryData[i]>>5) {
		return 0
	}
	kind := int(unicodeCategoryData[i] & 31)
	if kind == 30 {
		if (key-unicodeCategoryMap[i])&1 != 0 {
			return 5
		}
		return 9
	}
	return kind
}

func unicode61Diacritic(code rune) bool {
	if code < 768 || code > 817 {
		return false
	}
	if code < 800 {
		return uint32(0x08029FDF)&(1<<uint(code-768)) != 0
	}
	return uint32(0x000361F8)&(1<<uint(code-800)) != 0
}

func unicode61Fold(code rune, remove int) rune {
	if code < 128 {
		if code >= 'A' && code <= 'Z' {
			return code + 32
		}
		return code
	}
	if code >= 65536 {
		if code >= 66560 && code < 66600 {
			return code + 40
		}
		return code
	}
	i := sort.Search(len(unicodeFoldRanges), func(i int) bool { return rune(unicodeFoldRanges[i][0]) > code }) - 1
	if i >= 0 {
		r := unicodeFoldRanges[i]
		if code < rune(r[0]+r[2]) && (1&r[1]&uint16(rune(r[0])^code)) == 0 {
			code = (code + rune(unicodeFoldOffsets[r[1]>>1])) & 0xffff
		}
	}
	if remove != 0 {
		key := uint32(code)<<3 | 7
		i := sort.Search(len(unicodeDiacriticRanges), func(i int) bool { return uint32(unicodeDiacriticRanges[i]) > key }) - 1
		ch := unicodeDiacriticChars[i]
		if !(remove == 1 && ch&128 != 0) && code <= rune(unicodeDiacriticRanges[i]>>3)+rune(unicodeDiacriticRanges[i]&7) {
			code = rune(ch & 127)
		}
	}
	return code
}
