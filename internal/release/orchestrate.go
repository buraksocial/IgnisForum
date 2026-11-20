package release

import (
	"errors"
	"fmt"
)

// OrchestrationHooks are deliberately supplied by the operator integration.
// This package never invokes systemctl, shells, database commands or the web
// process directly.
type OrchestrationHooks struct {
	Stop         func() error
	Start        func() error
	Backup       func() error
	VerifyBackup func() error
	Health       func() error
}

// Orchestrate performs the safety sequence around Promote. Backup and its
// verification happen while the service is stopped. Start is called only from
// Promote's health gate, after the new binary is active. If promotion/start or
// health fails, Promote restores the old binary and this function attempts to
// restart the restored version.
//
// The callbacks must be idempotent: Stop may be called during recovery after a
// failed Start, and Start may be called again for the restored binary.
func Orchestrate(options PromotionOptions, hooks OrchestrationHooks) (PromotionResult, error) {
	if options.Health != nil {
		return PromotionResult{}, errors.New("promotion health must be supplied through orchestration hooks")
	}
	if hooks.Stop == nil || hooks.Start == nil || hooks.Backup == nil || hooks.VerifyBackup == nil || hooks.Health == nil {
		return PromotionResult{}, errors.New("stop, start, backup, backup verification and health hooks are required")
	}
	if err := hooks.Stop(); err != nil {
		return PromotionResult{}, fmt.Errorf("stop service: %w", err)
	}
	if err := hooks.Backup(); err != nil {
		return restartAfterFailure(hooks, fmt.Errorf("create backup: %w", err))
	}
	if err := hooks.VerifyBackup(); err != nil {
		return restartAfterFailure(hooks, fmt.Errorf("verify backup: %w", err))
	}
	options.Health = func() error {
		if err := hooks.Start(); err != nil {
			return fmt.Errorf("start promoted service: %w", err)
		}
		if err := hooks.Health(); err != nil {
			return fmt.Errorf("promoted service health: %w", err)
		}
		return nil
	}
	result, err := Promote(options)
	if err == nil {
		return result, nil
	}
	// Promote has already restored the binary when its health callback failed.
	// Stop any partially started process and bring the restored version up.
	if recoveryErr := restartRestored(hooks); recoveryErr != nil {
		return result, fmt.Errorf("promotion failed: %v; restore restart failed: %w", err, recoveryErr)
	}
	return result, err
}

func restartAfterFailure(hooks OrchestrationHooks, cause error) (PromotionResult, error) {
	if err := restartRestored(hooks); err != nil {
		return PromotionResult{}, fmt.Errorf("%v; restore restart failed: %w", cause, err)
	}
	return PromotionResult{}, cause
}

func restartRestored(hooks OrchestrationHooks) error {
	if err := hooks.Stop(); err != nil {
		return err
	}
	if err := hooks.Start(); err != nil {
		return err
	}
	return nil
}
