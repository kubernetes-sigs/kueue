package jobframework

// JobReconciler event reason list
const (
	Started            = "Started"
	Suspended          = "Suspended"
	Stopped            = "Stopped"
	CreatedWorkload    = "CreatedWorkload"
	DeletedWorkload    = "DeletedWorkload"
	UpdatedWorkload    = "UpdatedWorkload"
	FinishedWorkload   = "FinishedWorkload"
	ErrWorkloadCompose = "ErrWorkloadCompose"
)
