package main

// signalAction is the platform-neutral set of signals memhog sends.
type signalAction int

const (
	actTerm signalAction = iota
	actKill
	actStop
	actCont
)
