// Package eval's posting-door fingerprints.
//
// The file is Lappi's qd-posting-heldout.v1: 8-word grams, 128 MinHash permutations,
// BLAKE2b. The word class here is ASCII [0-9A-Za-z_], lower-cased, which matches
// Python \w and str.lower on ASCII job text. Python's Unicode \w tables are not
// copied into this package. The door in Lappi remains the judge at train time.
package eval

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// DoorFormat is the posting door's format string.
	DoorFormat = "qd-posting-heldout.v1"
	// DoorNgramN is the door's word-gram width.
	DoorNgramN = 8
	// DoorNumPerm is the door's permutation count.
	DoorNumPerm = 128
	// DoorSeed is the door CLI's default seed.
	DoorSeed = uint64(20260919)
)

// DoorPosting is one held-out posting the fingerprint export signs.
type DoorPosting struct {
	Key  string
	Text string
}

const blakeBlock = 128

var blakeIV = [8]uint64{
	0x6a09e667f3bcc908,
	0xbb67ae8584caa73b,
	0x3c6ef372fe94f82b,
	0xa54ff53a5f1d36f1,
	0x510e527fade682d1,
	0x9b05688c2b3e6c1f,
	0x1f83d9abfb41bd6b,
	0x5be0cd19137e2179,
}

var blakeSigma = [12][16]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
	{11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4},
	{7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
	{9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13},
	{2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
	{12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11},
	{13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
	{6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5},
	{10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
}

func blakeG(v *[16]uint64, a, b, c, d int, x, y uint64) {
	v[a] = v[a] + v[b] + x
	v[d] = bitsRotate(v[d]^v[a], 32)
	v[c] = v[c] + v[d]
	v[b] = bitsRotate(v[b]^v[c], 24)
	v[a] = v[a] + v[b] + y
	v[d] = bitsRotate(v[d]^v[a], 16)
	v[c] = v[c] + v[d]
	v[b] = bitsRotate(v[b]^v[c], 63)
}

func bitsRotate(x uint64, n uint) uint64 {
	return (x >> n) | (x << (64 - n))
}

func blakeCompress(h *[8]uint64, block *[blakeBlock]byte, t uint64, last bool) {
	var m [16]uint64
	for i := range m {
		m[i] = binary.LittleEndian.Uint64(block[i*8 : i*8+8])
	}
	var v [16]uint64
	copy(v[:8], h[:])
	copy(v[8:], blakeIV[:])
	v[12] ^= t
	if last {
		v[14] = ^v[14]
	}
	for s := 0; s < 12; s++ {
		sig := &blakeSigma[s]
		blakeG(&v, 0, 4, 8, 12, m[sig[0]], m[sig[1]])
		blakeG(&v, 1, 5, 9, 13, m[sig[2]], m[sig[3]])
		blakeG(&v, 2, 6, 10, 14, m[sig[4]], m[sig[5]])
		blakeG(&v, 3, 7, 11, 15, m[sig[6]], m[sig[7]])
		blakeG(&v, 0, 5, 10, 15, m[sig[8]], m[sig[9]])
		blakeG(&v, 1, 6, 11, 12, m[sig[10]], m[sig[11]])
		blakeG(&v, 2, 7, 8, 13, m[sig[12]], m[sig[13]])
		blakeG(&v, 3, 4, 9, 14, m[sig[14]], m[sig[15]])
	}
	for i := 0; i < 8; i++ {
		h[i] ^= v[i] ^ v[i+8]
	}
}

// blake2bKeyed is one (key, digest length), with the key block already absorbed.
type blake2bKeyed struct {
	param     uint64
	hAfterKey [8]uint64
	keyBlock  *[blakeBlock]byte
	outLen    int
}

func newBlake2b(key []byte, outLen int) blake2bKeyed {
	param := uint64(0x01010000) ^ (uint64(len(key)) << 8) ^ uint64(outLen)
	h := blakeIV
	h[0] ^= param
	var keyBlock *[blakeBlock]byte
	if len(key) > 0 {
		var block [blakeBlock]byte
		copy(block[:], key)
		blakeCompress(&h, &block, blakeBlock, false)
		keyBlock = &block
	}
	return blake2bKeyed{param: param, hAfterKey: h, keyBlock: keyBlock, outLen: outLen}
}

func (k blake2bKeyed) state(msg []byte) [8]uint64 {
	if len(msg) == 0 {
		h := blakeIV
		h[0] ^= k.param
		if k.keyBlock != nil {
			blakeCompress(&h, k.keyBlock, blakeBlock, true)
		} else {
			var zero [blakeBlock]byte
			blakeCompress(&h, &zero, 0, true)
		}
		return h
	}
	h := k.hAfterKey
	var t uint64
	if k.keyBlock != nil {
		t = blakeBlock
	}
	rest := msg
	for len(rest) > blakeBlock {
		var block [blakeBlock]byte
		copy(block[:], rest[:blakeBlock])
		t += blakeBlock
		blakeCompress(&h, &block, t, false)
		rest = rest[blakeBlock:]
	}
	var block [blakeBlock]byte
	copy(block[:], rest)
	t += uint64(len(rest))
	blakeCompress(&h, &block, t, true)
	return h
}

func (k blake2bKeyed) digest(msg []byte) []byte {
	h := k.state(msg)
	out := make([]byte, 0, 64)
	var word [8]byte
	for _, w := range h {
		binary.LittleEndian.PutUint64(word[:], w)
		out = append(out, word[:]...)
	}
	return out[:k.outLen]
}

func (k blake2bKeyed) u64BE(msg []byte) uint64 {
	return binary.BigEndian.Uint64(k.digest(msg))
}

func asciiWords(text string) []string {
	lower := strings.ToLower(text)
	var words []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		words = append(words, b.String())
		b.Reset()
	}
	for _, r := range lower {
		if r < 128 && (r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words
}

func wordGrams(text string) []uint64 {
	words := asciiWords(text)
	if len(words) < DoorNgramN {
		return nil
	}
	hasher := newBlake2b(nil, 8)
	out := make([]uint64, 0, len(words)-DoorNgramN+1)
	for i := 0; i+DoorNgramN <= len(words); i++ {
		joined := strings.Join(words[i:i+DoorNgramN], " ")
		out = append(out, hasher.u64BE([]byte(joined)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for _, v := range out {
		if n == 0 || out[n-1] != v {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

func mersenne() *big.Int {
	p := new(big.Int).Lsh(big.NewInt(1), 61)
	return p.Sub(p, big.NewInt(1))
}

func minhash(seed uint64, grams []uint64) []uint64 {
	material := fmt.Sprintf("qd_data.minhash.v1|%d", seed)
	key := newBlake2b(nil, 32).digest([]byte(material))
	keyed := newBlake2b(key, 32)
	p := mersenne()
	pm1 := new(big.Int).Sub(new(big.Int).Set(p), big.NewInt(1))
	a := make([]uint64, DoorNumPerm)
	b := make([]uint64, DoorNumPerm)
	var idx [8]byte
	for i := 0; i < DoorNumPerm; i++ {
		binary.BigEndian.PutUint64(idx[:], uint64(i))
		blob := keyed.digest(idx[:])
		hi := new(big.Int).SetBytes(blob[:16])
		lo := new(big.Int).SetBytes(blob[16:])
		ai := new(big.Int).Mod(hi, pm1)
		ai.Add(ai, big.NewInt(1))
		bi := new(big.Int).Mod(lo, p)
		a[i] = ai.Uint64()
		b[i] = bi.Uint64()
	}
	sigHasher := newBlake2b(key, 8)
	sig := make([]uint64, DoorNumPerm)
	for i := range sig {
		sig[i] = ^uint64(0)
	}
	var gram [8]byte
	for _, g := range grams {
		binary.BigEndian.PutUint64(gram[:], g)
		h := new(big.Int).SetUint64(sigHasher.u64BE(gram[:]))
		for i := 0; i < DoorNumPerm; i++ {
			v := new(big.Int).SetUint64(a[i])
			v.Mul(v, h)
			v.Add(v, new(big.Int).SetUint64(b[i]))
			v.Mod(v, p)
			if got := v.Uint64(); got < sig[i] {
				sig[i] = got
			}
		}
	}
	return sig
}

type doorFile struct {
	Format  string      `json:"format"`
	NgramN  int         `json:"ngram_n"`
	NumPerm int         `json:"num_perm"`
	Seed    uint64      `json:"seed"`
	Digest  string      `json:"digest"`
	Entries []doorEntry `json:"entries"`
}

type doorEntry struct {
	Key       string   `json:"key"`
	Ngrams    uint64   `json:"n_ngrams"`
	Signature []string `json:"signature"`
}

func doorDigest(seed uint64, entries []doorEntry, sigs [][]uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%d|%d\x1d", DoorFormat, DoorNgramN, DoorNumPerm, seed)
	for i, e := range entries {
		parts := make([]string, len(sigs[i]))
		for j, v := range sigs[i] {
			parts[j] = fmt.Sprintf("%016x", v)
		}
		fmt.Fprintf(&b, "%s\x1f%d\x1f%s\x1e", e.Key, e.Ngrams, strings.Join(parts, ","))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ExportDoorFingerprints writes a qd-posting-heldout.v1 document. An empty
// list, or a posting with no 8-gram, is an error and writes nothing.
func ExportDoorFingerprints(postings []DoorPosting, seed uint64) ([]byte, error) {
	if len(postings) == 0 {
		return nil, fmt.Errorf("eval: no held-out posting to fingerprint")
	}
	entries := make([]doorEntry, 0, len(postings))
	sigs := make([][]uint64, 0, len(postings))
	for _, p := range postings {
		if strings.TrimSpace(p.Key) == "" {
			return nil, fmt.Errorf("eval: a held-out posting has an empty key")
		}
		grams := wordGrams(p.Text)
		if len(grams) == 0 {
			return nil, fmt.Errorf("eval: held-out posting %s has no 8-gram", p.Key)
		}
		sig := minhash(seed, grams)
		hexSig := make([]string, len(sig))
		for i, v := range sig {
			hexSig[i] = fmt.Sprintf("%016x", v)
		}
		entries = append(entries, doorEntry{Key: p.Key, Ngrams: uint64(len(grams)), Signature: hexSig})
		sigs = append(sigs, sig)
	}
	doc := doorFile{
		Format:  DoorFormat,
		NgramN:  DoorNgramN,
		NumPerm: DoorNumPerm,
		Seed:    seed,
		Digest:  doorDigest(seed, entries, sigs),
		Entries: entries,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	return raw, nil
}

// WriteDoorExport replaces path with a fingerprint document.
func WriteDoorExport(path string, data []byte) error {
	return atomicfile.Write(path, data)
}

// ExportLabelledFingerprints signs the labelled postings' text. Missing text
// is reported and left out. No usable posting is an error.
func ExportLabelledFingerprints(s *Store, ledgerText func(posting.Key) (string, bool), seed uint64) ([]byte, []posting.Key, error) {
	all, bad, err := s.Labels()
	if err != nil {
		return nil, nil, err
	}
	if bad > 0 {
		return nil, nil, fmt.Errorf("eval: %d malformed label lines; refusing to export a partial held-out set", bad)
	}
	keys := make([]posting.Key, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var posts []DoorPosting
	var missing []posting.Key
	for _, k := range keys {
		text, ok := "", false
		if ledgerText != nil {
			text, ok = ledgerText(k)
		}
		if !ok || strings.TrimSpace(text) == "" {
			if h := all[k].TextSHA256; h != "" {
				if t, err := s.Text(h); err == nil {
					text, ok = t, true
				}
			}
		}
		if !ok || strings.TrimSpace(text) == "" {
			missing = append(missing, k)
			continue
		}
		posts = append(posts, DoorPosting{Key: string(k), Text: text})
	}
	if len(posts) == 0 {
		return nil, missing, fmt.Errorf("eval: no held-out posting text to fingerprint")
	}
	raw, err := ExportDoorFingerprints(posts, seed)
	return raw, missing, err
}
