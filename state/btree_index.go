package state

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path"
	"time"

	"github.com/google/btree"

	"github.com/ledgerwatch/erigon-lib/mmap"
)

type BtIndex struct {
	bt         *btree.BTreeG[uint64]
	mmapWin    *[mmap.MaxMapSize]byte
	mmapUnix   []byte
	data       []byte
	file       *os.File
	size       int64
	modTime    time.Time
	filePath   string
	keyCount   uint64
	baseDataID uint64
}

type page struct {
	i     uint64
	keys  uint64
	size  uint64
	nodes []*node
}

type node struct {
	key    []byte
	pos    uint64
	val    uint64
	parent uint64
}

type inode struct {
	page *page
	node *node
}

type cursor struct {
	stack []inode
}

func isEven(n uint64) bool {
	return n&1 == 0
}

type btAlloc struct {
	d       uint64     // depth
	M       uint64     // child limit of any node
	vx      []uint64   // vertex count on level
	sons    [][]uint64 // i - level; 0 <= i < d; j_k - amount, j_k+1 - child count
	cursors []cur
}

func logBase(n, base uint64) uint64 {
	return uint64(math.Ceil(math.Log(float64(n)) / math.Log(float64(base))))
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func newBtAlloc(k, M uint64) *btAlloc {
	var d uint64
	ks := k + 1
	d = logBase(ks, M)
	m := M >> 1

	fmt.Printf("k=%d d=%d, M=%d m=%d\n", k, d, M, m)
	a := &btAlloc{
		vx:      make([]uint64, d+1),
		sons:    make([][]uint64, d+1),
		cursors: make([]cur, d),
		M:       M, d: d,
	}
	a.vx[0] = 1
	a.vx[d] = ks

	nnc := func(vx uint64) uint64 {
		return vx / M
	}

	for i := a.d - 1; i > 0; i-- {
		//nnc := uint64(math.Ceil(float64(a.vx[i+1]) / float64(M))+1)
		//nvc := uint64(math.Floor(float64(a.vx[i+1]) / float64(m))-1)
		//nnc := a.vx[i+1] / M
		//nvc := a.vx[i+1] / m
		bvc := a.vx[i+1] / (m + (m >> 1))
		//_, _ = nvc, nnc
		a.vx[i] = min64(uint64(math.Pow(float64(M), float64(i))), bvc)
	}

	pnv := uint64(0)
	for l := a.d - 1; l > 0; l-- {
		s := nnc(a.vx[l+1])
		ncount := uint64(0)
		left := a.vx[l+1] % M
		if pnv != 0 {
			s = nnc(pnv)
			left = pnv % M
		}
		if left > 0 {
			if left < m {
				s--
				newPrev := M - (m - left)
				dp := M - newPrev
				a.sons[l] = append(a.sons[l], 1, newPrev, 1, left+dp)
			} else {
				a.sons[l] = append(a.sons[l], 1, left)
			}
		}
		a.sons[l] = append(a.sons[l], s, M)
		for i := 0; i < len(a.sons[l]); i += 2 {
			ncount += a.sons[l][i]
		}
		pnv = ncount

	}
	a.sons[0] = []uint64{1, pnv}

	for i, v := range a.sons {
		fmt.Printf("L%d=%v\n", i, v)
	}

	return a
}

type cur struct {
	l, p, di, si uint64

	//l - level
	//p - pos inside level
	//si - current, actual son index
	//di - data array index
}

func (a *btAlloc) traverse() {
	var sum uint64
	for l := 0; l < len(a.sons)-1; l++ {
		if len(a.sons[l]) < 2 {
			panic("invalid btree allocation markup")
		}
		a.cursors[l] = cur{uint64(l), 1, 0, 0}

		for i := 0; i < len(a.sons[l]); i += 2 {
			sum += a.sons[l][i] * a.sons[l][i+1]
		}
	}
	fmt.Printf("nodes total %d\n", sum)

	c := a.cursors[len(a.cursors)-1]

	var di uint64
	for stop := false; !stop; {
		bros := a.sons[c.l][c.p]
		parents := a.sons[c.l][c.p-1]

		// fill leaves, mark parent if needed (until all grandparents not marked up until root)
		// check if eldest parent has brothers
		//     -- has bros -> fill their leaves from the bottom
		//     -- no bros  -> shift cursor (tricky)

		for i := uint64(0); i < bros; i++ {
			c.si++
			c.di = di
			di++
			fmt.Printf("L{%d,%d| d %d s %d}\n", c.l, c.p, c.di, c.si)
		}

		pid := c.si / bros
		if pid >= parents {
			if c.p+2 >= uint64(len(a.sons[c.l])) {
				stop = true // end of row
			}
			fmt.Printf("N %d d%d s%d\n", c.l, c.di, c.si)
			c.p += 2
			c.si = 1
			a.cursors[c.l] = c
		}

		for l := len(a.cursors) - 2; l >= 0; l-- {
			pc := a.cursors[l]
			uncles := a.sons[pc.l][pc.p]
			grands := a.sons[pc.l][pc.p-1]

			pi1 := pc.si / uncles
			pc.si++
			pi2 := pc.si / uncles

			pc.di = di
			di++

			if pi2 >= grands {
				if pc.p+2 >= uint64(len(a.sons[pc.l])) {
					// end of row
					break
				}
				fmt.Printf("N %d d%d s%d\n", pc.l, pc.di, pc.si)
				pc.p += 2
				pc.si = 1
				pc.di = di
			}
			a.cursors[pc.l] = pc

			fmt.Printf("P{%d,%d| %d s=%d} pid %d\n", pc.l, pc.p, pc.di, pc.si, pid)

			if pc.si > 1 && pi2-pi1 == 0 {
				break
			}
		}

	}
}


func OpenBtreeIndex(indexPath string) (*BtIndex, error) {
	s, err := os.Stat(indexPath)
	if err != nil {
		return nil, err
	}

	idx := &BtIndex{
		filePath: indexPath,
		size:     s.Size(),
		modTime:  s.ModTime(),
		//idx:      btree.NewG[uint64](32, commitmentItemLess),
	}

	idx.file, err = os.Open(indexPath)
	if err != nil {
		return nil, err
	}

	if idx.mmapUnix, idx.mmapWin, err = mmap.Mmap(idx.file, int(idx.size)); err != nil {
		return nil, err
	}
	idx.data = idx.mmapUnix[:idx.size]
	// Read number of keys and bytes per record
	idx.baseDataID = binary.BigEndian.Uint64(idx.data[:8])
	idx.keyCount = binary.BigEndian.Uint64(idx.data[8:16])
	return idx, nil
}

func (b *BtIndex) Size() int64 { return b.size }

func (b *BtIndex) ModTime() time.Time { return b.modTime }

func (b *BtIndex) BaseDataID() uint64 { return b.baseDataID }

func (b *BtIndex) FilePath() string { return b.filePath }

func (b *BtIndex) FileName() string { return path.Base(b.filePath) }

func (b *BtIndex) Empty() bool { return b.keyCount == 0 }

func (b *BtIndex) KeyCount() uint64 { return b.keyCount }

func (b *BtIndex) Close() error {
	if b == nil {
		return nil
	}
	if err := mmap.Munmap(b.mmapUnix, b.mmapWin); err != nil {
		return err
	}
	if err := b.file.Close(); err != nil {
		return err
	}
	return nil
}

func (b *BtIndex) Lookup(bucketHash, fingerprint uint64) uint64 {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) OrdinalLookup(i uint64) uint64 {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) ExtractOffsets() map[uint64]uint64 {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) RewriteWithOffsets(w *bufio.Writer, m map[uint64]uint64) error {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) DisableReadAhead() {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) EnableReadAhead() *interface{} {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) EnableMadvNormal() *interface{} {
	//TODO implement me
	panic("implement me")
}

func (b *BtIndex) EnableWillNeed() *interface{} {
	//TODO implement me
	panic("implement me")
}
