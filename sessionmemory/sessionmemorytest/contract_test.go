package sessionmemorytest

import (
	"testing"

	"github.com/normahq/balda/sessionmemory"
)

func TestStoreContract(t *testing.T) {
	RunStoreContract(t, func() sessionmemory.Store { return NewStore() })
}
