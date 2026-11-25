package exporter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Pure-Go implementation of Haruhiko Okumura's LHA -lh5- (AR002) codec with the
// 4-byte little-endian uncompressed length header used by ZKTeco ZKTime 5.0 (.ABT).

const (
	lh5DicBit    = 13
	lh5DicSiz    = 1 << lh5DicBit // 8192
	lh5MaxMatch  = 256
	lh5Threshold = 3
	lh5NC        = 255 + lh5MaxMatch + 2 - lh5Threshold // 510
	lh5CBit      = 9
	lh5CodeBit   = 16
	lh5NP        = lh5DicBit + 1  // 14
	lh5NT        = lh5CodeBit + 3 // 19
	lh5PBit      = 4
	lh5TBit      = 5
	lh5NPT       = lh5NT // max(NT, NP) = 19

	lh5MaxOrigSize = 256 * 1024 * 1024 // 256 MiB safety cap for decoder
	lh5BlockLimit  = 16384             // symbols per Huffman block (<= 65535)
)

// DecodeLH5 decodes a ZKTime 5.0 .ABT byte stream (4-byte LE origSize + -lh5- stream).
func DecodeLH5(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, errors.New("ABT 文件过短，缺少长度头")
	}
	origSize := int(binary.LittleEndian.Uint32(data[:4]))
	if origSize < 0 || origSize > lh5MaxOrigSize {
		return nil, fmt.Errorf("ABT 文件头长度异常: %d", origSize)
	}
	if origSize == 0 {
		return []byte{}, nil
	}

	d := &lh5Decoder{
		comp: data[4:],
	}
	d.fillBuf(16)

	out := make([]byte, 0, origSize)
	var ring [lh5DicSiz]byte
	decodeJ := 0
	decodeI := 0

	for len(out) < origSize {
		rem := origSize - len(out)
		if rem > lh5DicSiz {
			rem = lh5DicSiz
		}
		r := 0
		for decodeJ > 0 {
			ring[r] = ring[decodeI]
			decodeI = (decodeI + 1) & (lh5DicSiz - 1)
			r++
			decodeJ--
			if r == rem {
				break
			}
		}
		for r < rem {
			c, err := d.decodeC()
			if err != nil {
				return nil, err
			}
			if c <= 255 {
				ring[r] = byte(c)
				r++
			} else {
				decodeJ = c - (256 - lh5Threshold)
				dist, err := d.decodeP()
				if err != nil {
					return nil, err
				}
				decodeI = (r - dist - 1) & (lh5DicSiz - 1)
				for decodeJ > 0 {
					ring[r] = ring[decodeI]
					decodeI = (decodeI + 1) & (lh5DicSiz - 1)
					r++
					decodeJ--
					if r == rem {
						break
					}
				}
			}
		}
		out = append(out, ring[:rem]...)
	}
	return out, nil
}

type lh5Decoder struct {
	comp      []byte
	pos       int
	bitBuf    uint16
	subBitBuf byte
	bitCount  int

	blockSize int
	left      [2*lh5NC - 1]uint16
	right     [2*lh5NC - 1]uint16
	ptLen     [lh5NPT]uint8
	ptTable   [256]uint16
	cLen      [lh5NC]uint8
	cTable    [4096]uint16
}

func (d *lh5Decoder) fillBuf(n int) {
	if n <= 0 {
		return
	}
	d.bitBuf <<= uint(n)
	for n > d.bitCount {
		n -= d.bitCount
		d.bitBuf |= uint16(d.subBitBuf) << uint(n)
		if d.pos < len(d.comp) {
			d.subBitBuf = d.comp[d.pos]
			d.pos++
		} else {
			d.subBitBuf = 0
		}
		d.bitCount = 8
	}
	d.bitCount -= n
	d.bitBuf |= uint16(d.subBitBuf) >> uint(d.bitCount)
}

func (d *lh5Decoder) getBits(n int) int {
	if n <= 0 {
		return 0
	}
	x := int(d.bitBuf >> uint(16-n))
	d.fillBuf(n)
	return x
}

func (d *lh5Decoder) makeTable(nchar int, bitLen []uint8, tableBits int, table []uint16) error {
	var count [17]uint16
	var weight [17]uint16
	var start [18]uint16

	for i := 0; i < nchar; i++ {
		if bitLen[i] > 16 {
			return fmt.Errorf("ABT 码流损坏：非法编码长度 %d", bitLen[i])
		}
		count[bitLen[i]]++
	}
	start[1] = 0
	for i := 1; i <= 16; i++ {
		start[i+1] = start[i] + (count[i] << uint(16-i))
	}
	if start[17] != 0 {
		return errors.New("ABT 码流损坏：Huffman 编码表校验失败")
	}

	jutBits := 16 - tableBits
	for i := 1; i <= tableBits; i++ {
		start[i] >>= uint(jutBits)
		weight[i] = 1 << uint(tableBits-i)
	}
	for i := tableBits + 1; i <= 16; i++ {
		weight[i] = 1 << uint(16-i)
	}

	if i := int(start[tableBits+1] >> uint(jutBits)); i != 0 {
		k := 1 << uint(tableBits)
		for i < k {
			table[i] = 0
			i++
		}
	}

	avail := uint16(nchar)
	mask := uint16(1) << uint(15-tableBits)
	maxNode := uint16(len(d.left))

	for ch := 0; ch < nchar; ch++ {
		l := int(bitLen[ch])
		if l == 0 {
			continue
		}
		nextCode := start[l] + weight[l]
		if l <= tableBits {
			if int(nextCode) > len(table) {
				return errors.New("ABT 码流损坏：解码表越界")
			}
			for i := start[l]; i < nextCode; i++ {
				table[i] = uint16(ch)
			}
		} else {
			k := start[l]
			p := table
			idx := int(k >> uint(jutBits))
			for rem := l - tableBits; rem > 0; rem-- {
				if p[idx] == 0 {
					if avail >= maxNode {
						return errors.New("ABT 码流损坏：解码树节点溢出")
					}
					d.right[avail] = 0
					d.left[avail] = 0
					p[idx] = avail
					avail++
				}
				node := int(p[idx])
				if k&mask != 0 {
					p = d.right[:]
				} else {
					p = d.left[:]
				}
				idx = node
				k <<= 1
			}
			p[idx] = uint16(ch)
		}
		start[l] = nextCode
	}
	return nil
}

func (d *lh5Decoder) readPTLen(nn, nbit, iSpecial int) error {
	n := d.getBits(nbit)
	if n == 0 {
		c := uint16(d.getBits(nbit))
		for i := 0; i < nn; i++ {
			d.ptLen[i] = 0
		}
		for i := range d.ptTable {
			d.ptTable[i] = c
		}
		return nil
	}
	if n > nn {
		return errors.New("ABT 码流损坏：PT 表长度越界")
	}
	i := 0
	for i < n {
		c := int(d.bitBuf >> 13)
		if c == 7 {
			mask := uint16(1) << 12
			for mask != 0 && (mask&d.bitBuf) != 0 {
				mask >>= 1
				c++
			}
		}
		if c < 7 {
			d.fillBuf(3)
		} else {
			d.fillBuf(c - 3)
		}
		if c > 16 {
			return errors.New("ABT 码流损坏：PT 码长超过 16")
		}
		d.ptLen[i] = uint8(c)
		i++
		if i == iSpecial {
			zeros := d.getBits(2)
			for zeros > 0 && i < nn {
				d.ptLen[i] = 0
				i++
				zeros--
			}
		}
	}
	for i < nn {
		d.ptLen[i] = 0
		i++
	}
	return d.makeTable(nn, d.ptLen[:nn], 8, d.ptTable[:])
}

func (d *lh5Decoder) readCLen() error {
	n := d.getBits(lh5CBit)
	if n == 0 {
		c := uint16(d.getBits(lh5CBit))
		for i := 0; i < lh5NC; i++ {
			d.cLen[i] = 0
		}
		for i := range d.cTable {
			d.cTable[i] = c
		}
		return nil
	}
	if n > lh5NC {
		return errors.New("ABT 码流损坏：C 表长度越界")
	}
	i := 0
	for i < n {
		c := int(d.ptTable[d.bitBuf>>8])
		if c >= lh5NT {
			mask := uint16(1) << 7
			for c >= lh5NT {
				if d.bitBuf&mask != 0 {
					c = int(d.right[c])
				} else {
					c = int(d.left[c])
				}
				mask >>= 1
				if mask == 0 && c >= lh5NT {
					return errors.New("ABT 码流损坏：C 表树解码失败")
				}
			}
		}
		d.fillBuf(int(d.ptLen[c]))
		if c <= 2 {
			var zeros int
			switch c {
			case 0:
				zeros = 1
			case 1:
				zeros = d.getBits(4) + 3
			default:
				zeros = d.getBits(lh5CBit) + 20
			}
			for zeros > 0 && i < lh5NC {
				d.cLen[i] = 0
				i++
				zeros--
			}
		} else {
			d.cLen[i] = uint8(c - 2)
			i++
		}
	}
	for i < lh5NC {
		d.cLen[i] = 0
		i++
	}
	return d.makeTable(lh5NC, d.cLen[:], 12, d.cTable[:])
}

func (d *lh5Decoder) decodeC() (int, error) {
	if d.blockSize == 0 {
		d.blockSize = d.getBits(16)
		if d.blockSize == 0 {
			return 0, errors.New("ABT 码流损坏：遇到空数据块")
		}
		if err := d.readPTLen(lh5NT, lh5TBit, 3); err != nil {
			return 0, err
		}
		if err := d.readCLen(); err != nil {
			return 0, err
		}
		if err := d.readPTLen(lh5NP, lh5PBit, -1); err != nil {
			return 0, err
		}
	}
	d.blockSize--
	j := int(d.cTable[d.bitBuf>>4])
	if j >= lh5NC {
		mask := uint16(1) << 3
		for j >= lh5NC {
			if d.bitBuf&mask != 0 {
				j = int(d.right[j])
			} else {
				j = int(d.left[j])
			}
			mask >>= 1
			if mask == 0 && j >= lh5NC {
				return 0, errors.New("ABT 码流损坏：符号解码失败")
			}
		}
	}
	d.fillBuf(int(d.cLen[j]))
	return j, nil
}

func (d *lh5Decoder) decodeP() (int, error) {
	j := int(d.ptTable[d.bitBuf>>8])
	if j >= lh5NP {
		mask := uint16(1) << 7
		for j >= lh5NP {
			if d.bitBuf&mask != 0 {
				j = int(d.right[j])
			} else {
				j = int(d.left[j])
			}
			mask >>= 1
			if mask == 0 && j >= lh5NP {
				return 0, errors.New("ABT 码流损坏：位置解码失败")
			}
		}
	}
	d.fillBuf(int(d.ptLen[j]))
	if j != 0 {
		j = (1 << uint(j-1)) + d.getBits(j-1)
	}
	return j, nil
}

// EncodeLH5 encodes src into a ZKTime 5.0 .ABT byte stream (4-byte LE origSize + -lh5- stream).
func EncodeLH5(src []byte) []byte {
	out := make([]byte, 4, len(src)/3+64)
	binary.LittleEndian.PutUint32(out[:4], uint32(len(src)))
	if len(src) == 0 {
		return out
	}

	e := &lh5Encoder{
		out:      out,
		bitCount: 8,
		items:    make([]lh5Symbol, 0, lh5BlockLimit),
	}

	// 3-byte hash chain LZSS finder over sliding window [i-DICSIZ, i-1]
	const hashBits = 14
	const hashSize = 1 << hashBits
	const hashMask = hashSize - 1
	const maxChain = 48

	head := make([]int, hashSize)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int, len(src))
	for i := range prev {
		prev[i] = -1
	}

	hash3 := func(p int) int {
		return ((int(src[p]) << 6) ^ (int(src[p+1]) << 3) ^ int(src[p+2])) & hashMask
	}
	insertHash := func(p int) {
		if p+2 >= len(src) {
			return
		}
		h := hash3(p)
		prev[p] = head[h]
		head[h] = p
	}
	findMatch := func(pos int) (bestLen int, bestDist int) {
		if pos+lh5Threshold > len(src) {
			return 0, 0
		}
		maxLen := len(src) - pos
		if maxLen > lh5MaxMatch {
			maxLen = lh5MaxMatch
		}
		h := hash3(pos)
		cand := head[h]
		minPos := pos - lh5DicSiz
		if minPos < 0 {
			minPos = 0
		}
		steps := 0
		for cand >= minPos && steps < maxChain {
			if bestLen == 0 || src[cand+bestLen] == src[pos+bestLen] {
				l := 0
				for l < maxLen && src[cand+l] == src[pos+l] {
					l++
				}
				if l >= lh5Threshold && l > bestLen {
					bestLen = l
					bestDist = pos - cand - 1
					if bestLen == maxLen {
						break
					}
				}
			}
			cand = prev[cand]
			steps++
		}
		return bestLen, bestDist
	}

	n := len(src)
	pos := 0
	for pos < n {
		mLen, mDist := findMatch(pos)
		if mLen >= lh5Threshold {
			// Lazy matching: check if pos+1 yields a longer match
			if mLen < lh5MaxMatch && pos+1 < n {
				insertHash(pos)
				nextLen, _ := findMatch(pos + 1)
				if nextLen > mLen+1 {
					e.addLiteral(src[pos])
					pos++
					continue
				}
			} else {
				insertHash(pos)
			}
			e.addMatch(mLen, mDist)
			for k := 1; k < mLen; k++ {
				insertHash(pos + k)
			}
			pos += mLen
		} else {
			insertHash(pos)
			e.addLiteral(src[pos])
			pos++
		}
	}

	e.flushBlock()
	e.putBits(7, 0) // flush any remaining bits in subBitBuf
	return e.out
}

type lh5Symbol struct {
	c    uint16
	dist uint16
}

type lh5Encoder struct {
	out       []byte
	subBitBuf byte
	bitCount  int

	items []lh5Symbol
	cFreq [2*lh5NC - 1]uint16
	pFreq [2*lh5NP - 1]uint16
	tFreq [2*lh5NT - 1]uint16

	cLen   [lh5NC]uint8
	cCode  [lh5NC]uint16
	ptLen  [lh5NPT]uint8
	ptCode [lh5NPT]uint16

	// Scratch for Huffman tree construction (AR002 maketree)
	left     [2*lh5NC - 1]uint16
	right    [2*lh5NC - 1]uint16
	heap     [lh5NC + 1]int
	heapSize int
	lenCnt   [17]uint16
}

func (e *lh5Encoder) putBits(n int, x uint16) {
	if n <= 0 {
		return
	}
	if n < e.bitCount {
		e.bitCount -= n
		e.subBitBuf |= byte(x << uint(e.bitCount))
		return
	}
	n -= e.bitCount
	e.out = append(e.out, e.subBitBuf|byte(x>>uint(n)))
	if n < 8 {
		e.bitCount = 8 - n
		e.subBitBuf = byte(x << uint(e.bitCount))
	} else {
		e.out = append(e.out, byte(x>>uint(n-8)))
		e.bitCount = 16 - n
		e.subBitBuf = byte(x << uint(e.bitCount))
	}
}

func (e *lh5Encoder) addLiteral(b byte) {
	c := uint16(b)
	e.items = append(e.items, lh5Symbol{c: c})
	e.cFreq[c]++
	if len(e.items) >= lh5BlockLimit {
		e.flushBlock()
	}
}

func (e *lh5Encoder) addMatch(matchLen, dist int) {
	c := uint16(matchLen + (256 - lh5Threshold))
	e.items = append(e.items, lh5Symbol{c: c, dist: uint16(dist)})
	e.cFreq[c]++
	pCode := posCode(dist)
	e.pFreq[pCode]++
	if len(e.items) >= lh5BlockLimit {
		e.flushBlock()
	}
}

func posCode(dist int) int {
	if dist == 0 {
		return 0
	}
	return bits.Len16(uint16(dist))
}

func (e *lh5Encoder) flushBlock() {
	if len(e.items) == 0 {
		return
	}
	e.putBits(16, uint16(len(e.items)))

	cRoot := e.makeTree(lh5NC, e.cFreq[:], e.cLen[:], e.cCode[:])
	e.countTFreq()
	tRoot := e.makeTree(lh5NT, e.tFreq[:], e.ptLen[:], e.ptCode[:])
	e.writePTLen(lh5NT, lh5TBit, 3, tRoot)
	e.writeCLen(cRoot)

	pRoot := e.makeTree(lh5NP, e.pFreq[:], e.ptLen[:], e.ptCode[:])
	e.writePTLen(lh5NP, lh5PBit, -1, pRoot)

	for _, sym := range e.items {
		e.putBits(int(e.cLen[sym.c]), e.cCode[sym.c])
		if sym.c >= 256 {
			pc := posCode(int(sym.dist))
			e.putBits(int(e.ptLen[pc]), e.ptCode[pc])
			if pc > 1 {
				eb := pc - 1
				mask := uint16((1 << uint(eb)) - 1)
				e.putBits(eb, sym.dist&mask)
			}
		}
	}

	// Reset block state
	e.items = e.items[:0]
	for i := 0; i < lh5NC; i++ {
		e.cFreq[i] = 0
	}
	for i := 0; i < lh5NP; i++ {
		e.pFreq[i] = 0
	}
}

func (e *lh5Encoder) countTFreq() {
	for i := 0; i < lh5NT; i++ {
		e.tFreq[i] = 0
	}
	n := lh5NC
	for n > 0 && e.cLen[n-1] == 0 {
		n--
	}
	i := 0
	for i < n {
		k := e.cLen[i]
		i++
		if k == 0 {
			count := 1
			for i < n && e.cLen[i] == 0 {
				i++
				count++
			}
			switch {
			case count <= 2:
				e.tFreq[0] += uint16(count)
			case count <= 18:
				e.tFreq[1]++
			case count == 19:
				e.tFreq[0]++
				e.tFreq[1]++
			default:
				e.tFreq[2]++
			}
		} else {
			e.tFreq[int(k)+2]++
		}
	}
}

func (e *lh5Encoder) writePTLen(nn, nbit, iSpecial, root int) {
	n := nn
	for n > 0 && e.ptLen[n-1] == 0 {
		n--
	}
	e.putBits(nbit, uint16(n))
	if n == 0 {
		e.putBits(nbit, uint16(root))
		return
	}
	i := 0
	for i < n {
		k := int(e.ptLen[i])
		i++
		if k < 7 {
			e.putBits(3, uint16(k))
		} else {
			e.putBits(k-3, uint16((1<<uint(k-3))-2))
		}
		if i == iSpecial {
			for i < 6 && e.ptLen[i] == 0 {
				i++
			}
			e.putBits(2, uint16(i-3))
		}
	}
}

func (e *lh5Encoder) writeCLen(cRoot int) {
	n := lh5NC
	for n > 0 && e.cLen[n-1] == 0 {
		n--
	}
	e.putBits(lh5CBit, uint16(n))
	if n == 0 {
		e.putBits(lh5CBit, uint16(cRoot))
		return
	}
	i := 0
	for i < n {
		k := int(e.cLen[i])
		i++
		if k == 0 {
			count := 1
			for i < n && e.cLen[i] == 0 {
				i++
				count++
			}
			switch {
			case count <= 2:
				for c := 0; c < count; c++ {
					e.putBits(int(e.ptLen[0]), e.ptCode[0])
				}
			case count <= 18:
				e.putBits(int(e.ptLen[1]), e.ptCode[1])
				e.putBits(4, uint16(count-3))
			case count == 19:
				e.putBits(int(e.ptLen[0]), e.ptCode[0])
				e.putBits(int(e.ptLen[1]), e.ptCode[1])
				e.putBits(4, 15)
			default:
				e.putBits(int(e.ptLen[2]), e.ptCode[2])
				e.putBits(lh5CBit, uint16(count-20))
			}
		} else {
			e.putBits(int(e.ptLen[k+2]), e.ptCode[k+2])
		}
	}
}

// makeTree builds a length-limited (<=16 bits) canonical Huffman tree, exact AR002 algorithm.
func (e *lh5Encoder) makeTree(nParm int, freq []uint16, bitLen []uint8, code []uint16) int {
	for i := 0; i < nParm; i++ {
		bitLen[i] = 0
	}
	avail := nParm
	e.heapSize = 0
	e.heap[1] = 0
	for i := 0; i < nParm; i++ {
		if freq[i] != 0 {
			e.heapSize++
			e.heap[e.heapSize] = i
		}
	}
	if e.heapSize < 2 {
		k := e.heap[1]
		code[k] = 0
		return k
	}
	for i := e.heapSize / 2; i >= 1; i-- {
		e.downHeap(i, freq)
	}

	sortIdx := make([]int, 0, e.heapSize)
	for e.heapSize > 1 {
		i := e.heap[1]
		if i < nParm {
			sortIdx = append(sortIdx, i)
		}
		e.heap[1] = e.heap[e.heapSize]
		e.heapSize--
		e.downHeap(1, freq)

		j := e.heap[1]
		if j < nParm {
			sortIdx = append(sortIdx, j)
		}
		k := avail
		avail++
		freq[k] = freq[i] + freq[j]
		e.heap[1] = k
		e.downHeap(1, freq)
		e.left[k] = uint16(i)
		e.right[k] = uint16(j)
	}
	root := e.heap[1]

	// Count leaf depths (clamped at 16)
	for i := range e.lenCnt {
		e.lenCnt[i] = 0
	}
	var countLen func(node, depth int)
	countLen = func(node, depth int) {
		if node < nParm {
			d := depth
			if d > 16 {
				d = 16
			}
			e.lenCnt[d]++
			return
		}
		countLen(int(e.left[node]), depth+1)
		countLen(int(e.right[node]), depth+1)
	}
	countLen(root, 0)

	// Kraft-inequality adjustment if any leaf exceeded depth 16
	var cum uint32
	for i := 16; i > 0; i-- {
		cum += uint32(e.lenCnt[i]) << uint(16-i)
	}
	for cum != (1 << 16) {
		e.lenCnt[16]--
		for i := 15; i > 0; i-- {
			if e.lenCnt[i] != 0 {
				e.lenCnt[i]--
				e.lenCnt[i+1] += 2
				break
			}
		}
		cum--
	}

	// Assign lengths to leaves from longest to shortest (lowest freq gets longest code)
	idx := 0
	for l := 16; l > 0; l-- {
		for k := e.lenCnt[l]; k > 0; k-- {
			bitLen[sortIdx[idx]] = uint8(l)
			idx++
		}
	}

	// Canonical codes
	var start [18]uint16
	for i := 1; i <= 16; i++ {
		start[i+1] = (start[i] + e.lenCnt[i]) << 1
	}
	for i := 0; i < nParm; i++ {
		if l := bitLen[i]; l > 0 {
			code[i] = start[l]
			start[l]++
		}
	}
	return root
}

func (e *lh5Encoder) downHeap(i int, freq []uint16) {
	k := e.heap[i]
	for {
		j := 2 * i
		if j > e.heapSize {
			break
		}
		if j < e.heapSize && freq[e.heap[j]] > freq[e.heap[j+1]] {
			j++
		}
		if freq[k] <= freq[e.heap[j]] {
			break
		}
		e.heap[i] = e.heap[j]
		i = j
	}
	e.heap[i] = k
}
