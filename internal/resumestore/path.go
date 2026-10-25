package resumestore

// PDFPath is where version n of key's resume keeps its PDF: an absolute path
// when the store's state directory is. It checks the key and the number only;
// the file exists when the version has a PDF (Meta.PDFSHA256 is not empty). The
// path names a 0600 file holding the owner's resume: a caller hands it on and
// never copies the bytes into JSON or a log.
func (s *Store) PDFPath(key string, n int) (string, error) {
	if err := CheckKey(key); err != nil {
		return "", err
	}
	if err := checkVersion(n); err != nil {
		return "", err
	}
	return versionFile(s.dir(key), n, "pdf"), nil
}
