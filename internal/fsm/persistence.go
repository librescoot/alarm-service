package fsm

import (
	"os"
	"path/filepath"
)

const defaultArmedStatePath = "/data/alarm-armed"

// Persistence handles saving and loading alarm armed state across power cycles
type Persistence interface {
	SaveArmedState(armed bool) error
	WasArmed() bool
}

// FilePersistence implements Persistence using a file flag
type FilePersistence struct {
	path string
}

// NewFilePersistence creates a FilePersistence with the default path
func NewFilePersistence() *FilePersistence {
	return &FilePersistence{path: defaultArmedStatePath}
}

// NewFilePersistenceWithPath creates a FilePersistence with a custom path (for testing)
func NewFilePersistenceWithPath(path string) *FilePersistence {
	return &FilePersistence{path: path}
}

func (p *FilePersistence) SaveArmedState(armed bool) error {
	if armed {
		dir := filepath.Dir(p.path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		tmp := p.path + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		f.Close()
		return os.Rename(tmp, p.path)
	}
	err := os.Remove(p.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *FilePersistence) WasArmed() bool {
	_, err := os.Stat(p.path)
	return err == nil
}

// NopPersistence is a no-op implementation for testing
type NopPersistence struct{}

func (p *NopPersistence) SaveArmedState(armed bool) error { return nil }
func (p *NopPersistence) WasArmed() bool                  { return false }
