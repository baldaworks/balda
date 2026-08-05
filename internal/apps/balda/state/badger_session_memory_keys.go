package state

import "github.com/normahq/balda/sessionmemory"

const (
	badgerSessionMemoryNamespace byte = 0x53
	badgerSessionMemoryVersion   byte = 1
	badgerRecordScope            byte = 1
	badgerRecordOperation        byte = 2
)

func badgerScopeKey(scope sessionmemory.Scope) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, string([]byte{badgerRecordScope}), scope.Key, string(scope.Kind))
}

func badgerOperationKey(scope sessionmemory.Scope, operationID string) ([]byte, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return sessionmemory.TupleKey(badgerSessionMemoryNamespace, badgerSessionMemoryVersion, string([]byte{badgerRecordOperation}), scope.Key, string(scope.Kind), operationID)
}
