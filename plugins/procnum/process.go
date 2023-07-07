package procnum

type PID int32

type PIDFinder interface {
	PidFile(path string) ([]PID, error)
	Pattern(pattern string, filters ...Filter) ([]PID, error)
	UID(user string) ([]PID, error)
	FullPattern(path string, filters ...Filter) ([]PID, error)
}
