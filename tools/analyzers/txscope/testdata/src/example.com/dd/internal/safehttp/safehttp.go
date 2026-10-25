package safehttp

import "example.com/dd/internal/store"

func Get() error { return nil }

// EnqueueTx is a transaction-scoped helper: it takes the transaction and only touches the database.
func EnqueueTx(tx *store.Tx) error { return nil }
