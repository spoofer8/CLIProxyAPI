package cliproxy

import sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"

func (s *Service) syncUserManagementAccess() {
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
