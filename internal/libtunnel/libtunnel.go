//go:build libopenconnect

package libtunnel

import "github.com/DavidVinu/uni-vpn/internal/octunnel"

type octEngine struct{}

func (octEngine) Connect(o octunnel.Options) (Session, error) {
	s, err := octunnel.Connect(o)
	if err != nil {
		return nil, err // not a typed nil in the interface
	}
	return s, nil
}

// Default is libopenconnect.
func Default() Engine { return octEngine{} }
