package store

type UserSvc struct {
	s *MemStore
}

func NewUserSvc(s *MemStore) *UserSvc {
	return &UserSvc{s: s}
}

func (u *UserSvc) Save(name, email string) error {
	return u.s.Put(name, email)
}

func (u *UserSvc) Lookup(name string) (string, error) {
	return u.s.Get(name)
}
