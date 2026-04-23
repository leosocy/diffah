package progress

import "github.com/opencontainers/go-digest"

func NewDiscard() Reporter { return discard{} }

type discard struct{}

func (discard) Phase(string)                                  {}
func (discard) StartLayer(digest.Digest, int64, string) Layer { return discardLayer{} }
func (discard) Finish()                                       {}

type discardLayer struct{}

func (discardLayer) Written(int64) {}
func (discardLayer) Done()         {}
func (discardLayer) Fail(error)    {}
