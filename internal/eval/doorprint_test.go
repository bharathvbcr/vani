package eval

import (
	"encoding/json"
	"os"
	"testing"
)

func TestBlake2bMatchesHashlib(t *testing.T) {
	empty := newBlake2b(nil, 64).digest(nil)
	if got := fmtHex(empty); got != "786a02f742015903c6c6fd852552d272912f4740e15847618a86e217f71f5419d25e1031afee585313896444934eb04b903a685b1448b755d56f701afe9be2ce" {
		t.Fatalf("empty blake2b: %s", got)
	}
	abc := newBlake2b(nil, 64).digest([]byte("abc"))
	if got := fmtHex(abc); got != "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d17d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923" {
		t.Fatalf("abc blake2b: %s", got)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	keyed := newBlake2b(key, 8).digest(nil)
	if got := fmtHex(keyed); got != "b6fbe0459eb623da" {
		t.Fatalf("keyed empty: %s", got)
	}
}

func TestExportDoorFingerprintFile(t *testing.T) {
	const text = "The senior platform engineer builds reliable systems for the team in the city office every week"
	raw, err := ExportDoorFingerprints([]DoorPosting{{Key: "held-1", Text: text}}, DoorSeed)
	if err != nil {
		t.Fatal(err)
	}
	var doc doorFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Format != DoorFormat || doc.NgramN != 8 || doc.NumPerm != 128 || doc.Seed != DoorSeed {
		t.Fatalf("header: %+v", doc)
	}
	if len(doc.Entries) != 1 || doc.Entries[0].Ngrams != 9 {
		t.Fatalf("n_ngrams: %+v", doc.Entries)
	}
	if doc.Entries[0].Signature[0] != "01c5dece6d155d9f" {
		t.Fatalf("sig0: %s", doc.Entries[0].Signature[0])
	}
	const want = "8e55d0f35df37ecc6c88a45abe67073d30bbeaa952233d987e67d8336efe711d"
	if doc.Digest != want {
		t.Fatalf("digest: %s", doc.Digest)
	}
	if path := os.Getenv("VANI_DOOR_EXPORT_OUT"); path != "" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fmtHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
