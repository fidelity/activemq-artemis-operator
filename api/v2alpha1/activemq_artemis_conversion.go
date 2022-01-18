package v2alpha1

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("v2alpha1Conversion")

func (r *ActiveMQArtemis) ConvertTo(dst conversion.Hub) error {
	log.V(1).Info("ConvertTo not implemented")
	return nil
}

func (r *ActiveMQArtemis) ConvertFrom(src conversion.Hub) error {
	log.V(1).Info("ConvertFrom not implemented")
	return nil
}
