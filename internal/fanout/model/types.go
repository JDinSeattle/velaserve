package model

type AcquisitionClass string

const (
	AcquisitionLocalHit  AcquisitionClass = "local-hit"
	AcquisitionP2PPull   AcquisitionClass = "p2p-pull"
	AcquisitionRecompute AcquisitionClass = "recompute"
)
