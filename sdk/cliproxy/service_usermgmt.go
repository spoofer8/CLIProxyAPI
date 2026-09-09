package cliproxy

import (
	"context"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

func (s *Service) syncUserManagementAccess() {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	s.userManagement.ConfigureBillingProviders(cfg)
	key := ""
	if cfg != nil && len(cfg.APIKeys) > 0 {
		key = cfg.APIKeys[0]
	}
	if errAdmin := s.userManagement.ConfigureMainAPIKey(context.Background(), key); errAdmin != nil {
		log.WithError(errAdmin).Error("configured Admin identity unavailable; proxy access will fail closed")
	}
	if s.accessManager == nil {
		return
	}
	provider := s.userManagement.AccessProvider()
	var priority []sdkaccess.Provider
	if provider != nil {
		priority = append(priority, provider)
	}
	s.accessManager.SetPriorityProviders(priority)
}
