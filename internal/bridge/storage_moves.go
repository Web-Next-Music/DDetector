package bridge

import (
	"fmt"
	"os"
)

type storageMove struct{ old, next string }
type storageRollbackError struct{ err error }

func (e *storageRollbackError) Error() string { return e.err.Error() }
func (e *storageRollbackError) Unwrap() error { return e.err }

func storageExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Each rename is atomic and refuses overwrites. If a later rename fails,
// restore earlier moves. A rollback collision stops startup rather than
// concealing split storage; all copies remain available for recovery.
func moveStorageFiles(moves []storageMove, rename func(string, string) error) error {
	for i, move := range moves {
		if err := rename(move.old, move.next); err != nil {
			for j := i - 1; j >= 0; j-- {
				if rollback := rename(moves[j].next, moves[j].old); rollback != nil {
					return &storageRollbackError{fmt.Errorf("storage migration failed (%v); rollback %s -> %s failed: %w", err, moves[j].next, moves[j].old, rollback)}
				}
			}
			return err
		}
	}
	return nil
}
