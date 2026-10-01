package backup

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
)

const chunkSize = 64 << 10

var magic = []byte("PIBASEB1")

type encryptWriter struct {
	out     io.Writer
	key     *[32]byte
	prefix  [16]byte
	counter uint64
	buffer  []byte
	closed  bool
}

func newEncryptWriter(out io.Writer, key [32]byte) (*encryptWriter, error) {
	w := &encryptWriter{out: out, key: &key}
	if _, err := rand.Read(w.prefix[:]); err != nil {
		return nil, err
	}
	if _, err := out.Write(append(magic, w.prefix[:]...)); err != nil {
		return nil, err
	}
	return w, nil
}
func (w *encryptWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("backup encryptor is closed")
	}
	total := len(p)
	w.buffer = append(w.buffer, p...)
	for len(w.buffer) >= chunkSize {
		if err := w.frame(w.buffer[:chunkSize]); err != nil {
			return 0, err
		}
		w.buffer = w.buffer[chunkSize:]
	}
	return total, nil
}
func (w *encryptWriter) frame(p []byte) error {
	var nonce [24]byte
	copy(nonce[:16], w.prefix[:])
	binary.BigEndian.PutUint64(nonce[16:], w.counter)
	w.counter++
	sealed := secretbox.Seal(nil, p, &nonce, w.key)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
	if _, err := w.out.Write(size[:]); err != nil {
		return err
	}
	_, err := w.out.Write(sealed)
	return err
}
func (w *encryptWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.frame(w.buffer); err != nil {
		return err
	}
	return w.frame(nil)
}

type decryptReader struct {
	in      *bufio.Reader
	key     *[32]byte
	prefix  [16]byte
	counter uint64
	current []byte
	done    bool
}

func newDecryptReader(in io.Reader, key [32]byte) (*decryptReader, error) {
	r := &decryptReader{in: bufio.NewReader(in), key: &key}
	header := make([]byte, len(magic)+16)
	if _, err := io.ReadFull(r.in, header); err != nil {
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if string(header[:len(magic)]) != string(magic) {
		return nil, fmt.Errorf("invalid backup format")
	}
	copy(r.prefix[:], header[len(magic):])
	return r, nil
}
func (r *decryptReader) Read(p []byte) (int, error) {
	for len(r.current) == 0 && !r.done {
		var size [4]byte
		if _, err := io.ReadFull(r.in, size[:]); err != nil {
			return 0, fmt.Errorf("truncated backup: %w", err)
		}
		n := binary.BigEndian.Uint32(size[:])
		if n < secretbox.Overhead || n > chunkSize+secretbox.Overhead {
			return 0, fmt.Errorf("invalid encrypted frame")
		}
		sealed := make([]byte, n)
		if _, err := io.ReadFull(r.in, sealed); err != nil {
			return 0, fmt.Errorf("truncated backup: %w", err)
		}
		var nonce [24]byte
		copy(nonce[:16], r.prefix[:])
		binary.BigEndian.PutUint64(nonce[16:], r.counter)
		r.counter++
		plain, ok := secretbox.Open(nil, sealed, &nonce, r.key)
		if !ok {
			return 0, fmt.Errorf("backup authentication failed")
		}
		if len(plain) == 0 {
			// The empty authenticated frame is the archive terminator. Do not
			// silently accept appended bytes: authenticated backups must have a
			// single, exact end so append-only tampering is detected too.
			if _, err := r.in.Peek(1); err != io.EOF {
				if err == nil {
					return 0, fmt.Errorf("trailing data after backup terminator")
				}
				return 0, fmt.Errorf("read backup terminator: %w", err)
			}
			r.done = true
			break
		}
		r.current = plain
	}
	if len(r.current) == 0 && r.done {
		return 0, io.EOF
	}
	n := copy(p, r.current)
	r.current = r.current[n:]
	return n, nil
}
