package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// Goal-layer failure classes. These are not the CG-3 executor classifier:
// they name launch/control/observation faults so attempts do not collapse
// into "sandbox failed" or a bare stream_incomplete.
const (
	goalFailPTYMissing            = "pty_missing"
	goalFailPTYIoctl              = "pty_ioctl"
	goalFailForegroundTTIN        = "foreground_sigttin"
	goalFailSlaveWriteNotControl  = "pty_slave_write_not_control"
	goalFailBudgetFlagLayer       = "budget_flag_layer"
	goalFailTUINotShell           = "tui_not_shell"
	goalFailInitGit               = "init_linked_git"
	goalFailApprovalDenied        = "approval_denied"
	goalFailPermission            = "permission_denied"
	goalFailContractControl       = "contract_control_mismatch"
	goalFailNativeSchema          = "native_schema_mismatch"
	goalFailNativeEOF             = "native_eof_without_terminal"
	goalFailProcessExitMismatch   = "process_exit_mismatch"
	goalFailDuplicateOwner        = "duplicate_owner"
	goalFailManagerUndelivered    = "manager_undelivered"
	goalFailPauseNotConfirmed     = "pause_not_confirmed"
	goalFailControlUnsupported    = "control_unsupported"
	goalFailExternalNoTakeover    = "external_no_takeover"
	goalFailStaleIdentity         = "stale_or_wrong_identity"
	goalFailPlanningUnknown       = "planning_failed_unknown"
	goalFailUnknown               = "unknown"
)

func classifyGoalLaunchError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errGoalUnsupportedTuple) {
		return goalFailContractControl
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "inappropriate ioctl"):
		return goalFailPTYIoctl
	case strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied") || errors.Is(err, syscall.EPERM):
		return goalFailPermission
	case strings.Contains(msg, "createprocess rejected") || strings.Contains(msg, "approval") && strings.Contains(msg, "denied"):
		return goalFailApprovalDenied
	case strings.Contains(msg, "sigttin") || strings.Contains(msg, "tty input"):
		return goalFailForegroundTTIN
	case strings.Contains(msg, "not a tty") || strings.Contains(msg, "no tty") || strings.Contains(msg, "pty_missing"):
		return goalFailPTYMissing
	case strings.Contains(msg, "unknown flag") && strings.Contains(msg, "budget"):
		return goalFailBudgetFlagLayer
	case strings.Contains(msg, "$(cat") || strings.Contains(msg, "tui is not a shell"):
		return goalFailTUINotShell
	case strings.Contains(msg, "linked") && strings.Contains(msg, "git"):
		return goalFailInitGit
	case strings.Contains(msg, "no such file") || errors.Is(err, os.ErrNotExist):
		return "executor_crash"
	default:
		return goalFailUnknown
	}
}

func classifyNativeObservationGap(obs nativeGoalObservation, processExited, custodyReleased bool) string {
	if obs.Contradictory {
		return goalFailNativeSchema
	}
	if obs.Missing && processExited {
		return goalFailNativeEOF
	}
	if processExited && !custodyReleased {
		return goalFailProcessExitMismatch
	}
	if strings.EqualFold(obs.NativeStatus, "paused") {
		return ""
	}
	return ""
}

func planningFailedUnknown(pauseMessage string) bool {
	msg := strings.ToLower(strings.TrimSpace(pauseMessage))
	return strings.Contains(msg, "planning failed")
}
