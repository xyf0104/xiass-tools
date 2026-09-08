package agent

// BuiltinMetadata returns the public integration matrix shown by XIASS Tools
// before platform-specific adapters are connected. These entries deliberately
// use generic metadata only: no third-party imagery, credentials, account
// state, or claim that an unbound action such as OAuth or quota refresh works.
func BuiltinMetadata() []Metadata {
	return []Metadata{
		{
			ID:          AntigravityID,
			DisplayName: "Antigravity",
			Vendor:      "Google",
			Category:    CategoryDesktopIDE,
			Description: "Desktop AI development environment integration profile.",
			Capabilities: []CapabilityDeclaration{
				bound(CapabilityDiscovery, "Discover supported local installations after a platform adapter is bound."),
				bound(CapabilityConfiguration, "Read and apply verified local integration configuration after binding."),
				bound(CapabilityLocalProxy, "Connect a verified local proxy configuration after binding."),
				bound(CapabilityPatchInjection, "Apply only a verified compatibility patch through a platform adapter."),
				bound(CapabilityModelCatalog, "Expose adapter-verified model configuration after binding."),
				bound(CapabilitySessionRecovery, "Recover supported local session data after binding."),
				bound(CapabilityImageIO, "Route verified image input and output support after binding."),
				bound(CapabilityDiagnostics, "Export target-specific diagnostics after binding."),
				bound(CapabilityBackup, "Create a transaction-safe local backup after binding."),
				notImplemented(CapabilityOAuth, "OAuth is not provided by the unbound agent registry."),
				notImplemented(CapabilityUsage, "Account usage refresh is not provided by the unbound agent registry."),
				notImplemented(CapabilityTwoFactorAuth, "Two-factor authentication is not provided by the unbound agent registry."),
			},
		},
		{
			ID:          CodexID,
			DisplayName: "Codex",
			Vendor:      "OpenAI",
			Category:    CategoryTerminal,
			Description: "Terminal coding agent integration profile.",
			Capabilities: []CapabilityDeclaration{
				bound(CapabilityDiscovery, "Discover supported local installations after a platform adapter is bound."),
				bound(CapabilityConfiguration, "Read and apply verified local configuration after binding."),
				bound(CapabilityLocalProxy, "Connect a verified local proxy configuration after binding."),
				bound(CapabilityModelCatalog, "Expose adapter-verified model configuration after binding."),
				bound(CapabilityDiagnostics, "Export target-specific diagnostics after binding."),
				bound(CapabilityBackup, "Create a transaction-safe local backup after binding."),
				notApplicable(CapabilityPatchInjection, "No patch-injection behavior is declared by this profile."),
				bound(CapabilitySessionRecovery, "Repair supported local provider-history compatibility only after explicit user confirmation."),
				notApplicable(CapabilityImageIO, "No image-routing behavior is declared by this profile."),
				notImplemented(CapabilityOAuth, "OAuth is not provided by the unbound agent registry."),
				notImplemented(CapabilityUsage, "Account usage refresh is not provided by the unbound agent registry."),
				notImplemented(CapabilityTwoFactorAuth, "Two-factor authentication is not provided by the unbound agent registry."),
			},
		},
		{
			ID:          ClaudeCodeID,
			DisplayName: "Claude Code",
			Vendor:      "Anthropic",
			Category:    CategoryTerminal,
			Description: "Terminal coding agent integration profile.",
			Capabilities: []CapabilityDeclaration{
				bound(CapabilityDiscovery, "Discover supported local installations after a platform adapter is bound."),
				bound(CapabilityConfiguration, "Read and apply verified local configuration after binding."),
				bound(CapabilityLocalProxy, "Connect a verified local proxy configuration after binding."),
				bound(CapabilityModelCatalog, "Expose adapter-verified model configuration after binding."),
				bound(CapabilityDiagnostics, "Export target-specific diagnostics after binding."),
				bound(CapabilityBackup, "Create a transaction-safe local backup after binding."),
				notApplicable(CapabilityPatchInjection, "No patch-injection behavior is declared by this profile."),
				notApplicable(CapabilitySessionRecovery, "No session-recovery behavior is declared by this profile."),
				notApplicable(CapabilityImageIO, "No image-routing behavior is declared by this profile."),
				notImplemented(CapabilityOAuth, "OAuth is not provided by the unbound agent registry."),
				notImplemented(CapabilityUsage, "Account usage refresh is not provided by the unbound agent registry."),
				notImplemented(CapabilityTwoFactorAuth, "Two-factor authentication is not provided by the unbound agent registry."),
			},
		},
		{
			ID:          CursorID,
			DisplayName: "Cursor",
			Vendor:      "Cursor",
			Category:    CategoryCodeEditor,
			Description: "Documented global and explicitly selected project MCP configuration integration profile; no account, OAuth, or session integration.",
			Capabilities: []CapabilityDeclaration{
				bound(CapabilityDiscovery, "Discover supported local installations after a platform adapter is bound."),
				bound(CapabilityConfiguration, "Read and apply the documented global MCP configuration and a user-selected project's .cursor/mcp.json after binding."),
				notImplemented(CapabilityLocalProxy, "This profile manages documented MCP configuration only; it does not provide a local proxy."),
				notImplemented(CapabilityModelCatalog, "This profile manages documented MCP configuration only; it does not provide a model catalog."),
				bound(CapabilityDiagnostics, "Export credential-free diagnostics for global MCP configuration; project MCP diagnostics require an explicit project selection."),
				notImplemented(CapabilityBackup, "Verified recovery points are created only during explicit global or user-selected project MCP configuration changes; no general agent backup capability is provided."),
				notApplicable(CapabilityPatchInjection, "No patch-injection behavior is declared by this profile."),
				notApplicable(CapabilitySessionRecovery, "No session-recovery behavior is declared by this profile."),
				notApplicable(CapabilityImageIO, "No image-routing behavior is declared by this profile."),
				notImplemented(CapabilityOAuth, "OAuth is not provided by the unbound agent registry."),
				notImplemented(CapabilityUsage, "Account usage refresh is not provided by the unbound agent registry."),
				notImplemented(CapabilityTwoFactorAuth, "Two-factor authentication is not provided by the unbound agent registry."),
			},
		},
		{
			ID:          WindsurfID,
			DisplayName: "Windsurf",
			Vendor:      "Windsurf",
			Category:    CategoryCodeEditor,
			Description: "Documented global MCP configuration integration profile; no account, OAuth, or session integration.",
			Capabilities: []CapabilityDeclaration{
				bound(CapabilityDiscovery, "Discover supported local installations after a platform adapter is bound."),
				bound(CapabilityConfiguration, "Read and apply only the documented global MCP configuration after binding."),
				notImplemented(CapabilityLocalProxy, "This profile manages global MCP configuration only; it does not provide a local proxy."),
				notImplemented(CapabilityModelCatalog, "This profile manages global MCP configuration only; it does not provide a model catalog."),
				bound(CapabilityDiagnostics, "Export credential-free diagnostics for documented global MCP configuration after binding."),
				notImplemented(CapabilityBackup, "Verified recovery points are created only during explicit global MCP configuration changes; no general agent backup capability is provided."),
				notApplicable(CapabilityPatchInjection, "No patch-injection behavior is declared by this profile."),
				notApplicable(CapabilitySessionRecovery, "No session-recovery behavior is declared by this profile."),
				notApplicable(CapabilityImageIO, "No image-routing behavior is declared by this profile."),
				notImplemented(CapabilityOAuth, "OAuth is not provided by the unbound agent registry."),
				notImplemented(CapabilityUsage, "Account usage refresh is not provided by the unbound agent registry."),
				notImplemented(CapabilityTwoFactorAuth, "Two-factor authentication is not provided by the unbound agent registry."),
			},
		},
	}
}

func bound(capability Capability, summary string) CapabilityDeclaration {
	return CapabilityDeclaration{
		Capability:   capability,
		Availability: CapabilityRequiresBinding,
		Summary:      summary,
	}
}

func notImplemented(capability Capability, summary string) CapabilityDeclaration {
	return CapabilityDeclaration{
		Capability:   capability,
		Availability: CapabilityNotImplemented,
		Summary:      summary,
	}
}

func notApplicable(capability Capability, summary string) CapabilityDeclaration {
	return CapabilityDeclaration{
		Capability:   capability,
		Availability: CapabilityNotApplicable,
		Summary:      summary,
	}
}
