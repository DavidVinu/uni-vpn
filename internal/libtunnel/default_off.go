//go:build !libopenconnect

package libtunnel

import "github.com/DavidVinu/uni-vpn/internal/octunnel"

type missingEngine struct{}

func (missingEngine) Connect(o octunnel.Options) (Session, error) {
	if o.Line != nil {
		o.Line("This uni-vpn was built without libopenconnect (build tag libopenconnect)")
	}
	return nil, &octunnel.ExitError{Code: 127, Reason: "built without libopenconnect"}
}

// Default fails in a build without the tag libopenconnect.
func Default() Engine { return missingEngine{} }
