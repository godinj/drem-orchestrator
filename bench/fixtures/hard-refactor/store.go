package store

import "fmt"

type MemStore struct {
	data map[string]string
}

func (m *MemStore) Get(k string) (string, error) {
	v, ok := m.data[k]
	if !ok {
		return "", fmt.Errorf("not found: %s", k)
	}
	return v, nil
}

func (m *MemStore) Put(k, v string) error {
	if m.data == nil {
		m.data = map[string]string{}
	}
	m.data[k] = v
	return nil
}
