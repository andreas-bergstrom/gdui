package git

type ChangeKind int

const (
	Modified ChangeKind = iota
	Added
	Deleted
	Renamed
	Untracked
	Conflicted // unmerged entry during a merge/rebase/cherry-pick
)

func (k ChangeKind) Letter() string {
	switch k {
	case Added:
		return "A"
	case Deleted:
		return "D"
	case Renamed:
		return "R"
	case Untracked:
		return "?"
	case Conflicted:
		return "U"
	default:
		return "M"
	}
}

type ChangedFile struct {
	Path    string
	OldPath string
	Kind    ChangeKind
	Adds    int
	Dels    int
	Binary  bool
}

type Hunk struct {
	Header string
	Lines  []DiffLine
}

type DiffLine struct {
	Kind          byte // ' ', '+', '-'
	Text          string
	NoNewlineHere bool // true if `\ No newline at end of file` followed this line
}
