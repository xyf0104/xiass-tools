package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

var (
	// ErrUnknownAgent is returned when a caller names no registered integration.
	ErrUnknownAgent = errors.New("agent is not registered")
	// ErrAdapterAlreadyBound prevents an accidental runtime adapter replacement.
	ErrAdapterAlreadyBound = errors.New("agent adapter is already bound")
	// ErrMetadataAlreadyRegistered prevents an accidental public-profile overwrite.
	ErrMetadataAlreadyRegistered = errors.New("agent metadata is already registered")
	// ErrAdapterUnbound means the public profile exists but no concrete adapter
	// has been connected to it yet.
	ErrAdapterUnbound = errors.New("agent adapter is not bound")
	// ErrCapabilityUnavailable is returned when an optional provider is absent.
	ErrCapabilityUnavailable = errors.New("agent capability is unavailable")
	// ErrNilAdapter prevents a typed nil or missing runtime binding.
	ErrNilAdapter = errors.New("agent adapter is nil")
)

type registryEntry struct {
	metadata Metadata
	adapter  Adapter
}

// Registry owns public agent metadata and optional runtime bindings. Its zero
// value is usable, although NewRegistry makes it easier to register validated
// built-ins at startup.
type Registry struct {
	mu      sync.RWMutex
	entries map[ID]registryEntry
	order   []ID
	now     func() time.Time
}

// AggregateStatus is the ordered result of a single cross-agent detection
// pass. One failed integration never prevents the rest from being inspected.
type AggregateStatus struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	State       State        `json:"state"`
	Agents      []Status     `json:"agents"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// NewRegistry constructs a registry with metadata-only entries. The metadata
// can later be connected to concrete implementations with Register or Replace.
func NewRegistry(metadata ...Metadata) (*Registry, error) {
	registry := &Registry{
		entries: make(map[ID]registryEntry),
		now:     time.Now,
	}
	for _, item := range metadata {
		if err := registry.RegisterMetadata(item); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewDefaultRegistry returns all known public XIASS Tools integration profiles
// in a deliberately unbound state. It never claims an OAuth flow, quota check,
// patch, or configuration operation is implemented merely because a profile is
// visible in the registry.
func NewDefaultRegistry() *Registry {
	registry, err := NewRegistry(BuiltinMetadata()...)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in agent metadata: %v", err))
	}
	return registry
}

// RegisterMetadata adds a public profile without binding implementation code.
func (r *Registry) RegisterMetadata(metadata Metadata) error {
	if err := metadata.Validate(); err != nil {
		return err
	}
	r.ensureInitialized()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[metadata.ID]; exists {
		return fmt.Errorf("%w: %s", ErrMetadataAlreadyRegistered, metadata.ID)
	}
	r.entries[metadata.ID] = registryEntry{metadata: metadata.Clone()}
	r.order = append(r.order, metadata.ID)
	return nil
}

// Register binds an adapter at runtime. If a built-in profile already exists,
// its public metadata is enriched by the adapter's metadata; a second binding
// requires an explicit Replace call.
func (r *Registry) Register(adapter Adapter) error {
	return r.bind(adapter, false)
}

// Bind is an expressive alias for Register used by platform startup code.
func (r *Registry) Bind(adapter Adapter) error {
	return r.Register(adapter)
}

// Replace intentionally replaces an existing runtime adapter. It is suitable
// for hot reload, a platform-specific override, or a test double.
func (r *Registry) Replace(adapter Adapter) error {
	return r.bind(adapter, true)
}

func (r *Registry) bind(adapter Adapter, replace bool) error {
	if isNilAdapter(adapter) {
		return ErrNilAdapter
	}
	metadata := adapter.Metadata()
	if err := metadata.Validate(); err != nil {
		return err
	}
	r.ensureInitialized()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.entries[metadata.ID]
	if exists && !isNilAdapter(entry.adapter) && !replace {
		return fmt.Errorf("%w: %s", ErrAdapterAlreadyBound, metadata.ID)
	}
	if !exists {
		r.order = append(r.order, metadata.ID)
		entry.metadata = metadata.Clone()
	} else {
		entry.metadata = mergeMetadata(entry.metadata, metadata)
	}
	entry.adapter = adapter
	r.entries[metadata.ID] = entry
	return nil
}

// Unbind leaves public metadata in place while disconnecting its runtime
// implementation. It returns false when no adapter was bound.
func (r *Registry) Unbind(id ID) bool {
	r.ensureInitialized()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.entries[id]
	if !exists || isNilAdapter(entry.adapter) {
		return false
	}
	entry.adapter = nil
	r.entries[id] = entry
	return true
}

// Metadata returns an isolated public profile for an agent.
func (r *Registry) Metadata(id ID) (Metadata, bool) {
	r.ensureInitialized()
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.entries[id]
	if !exists {
		return Metadata{}, false
	}
	return entry.metadata.Clone(), true
}

// List returns metadata in deterministic registration order.
func (r *Registry) List() []Metadata {
	r.ensureInitialized()
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Metadata, 0, len(r.order))
	for _, id := range r.order {
		if entry, exists := r.entries[id]; exists {
			items = append(items, entry.metadata.Clone())
		}
	}
	return items
}

// IsBound reports whether a concrete runtime adapter is registered.
func (r *Registry) IsBound(id ID) bool {
	_, adapter, exists := r.lookup(id)
	return exists && !isNilAdapter(adapter)
}

// Detect observes one integration. Unbound profiles return StateUnbound with
// every declared capability unavailable, rather than returning a false-ready
// result or a synthetic installation.
func (r *Registry) Detect(ctx context.Context, id ID) (Status, error) {
	metadata, adapter, exists := r.lookup(id)
	if !exists {
		return Status{}, fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	if isNilAdapter(adapter) {
		return unboundStatus(metadata, r.clock()), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := adapter.Detect(ctx)
	if err != nil {
		return errorStatus(metadata, status, err, r.clock()), err
	}
	if status.AgentID != "" && status.AgentID != metadata.ID {
		mismatch := fmt.Errorf("adapter returned status for %q, expected %q", status.AgentID, metadata.ID)
		return errorStatus(metadata, status, mismatch, r.clock()), mismatch
	}
	return normalizeStatus(metadata, status, r.clock()), nil
}

// DetectAll runs independent adapters concurrently and aggregates failures as
// diagnostics. The order of returned agents always matches List().
func (r *Registry) DetectAll(ctx context.Context) AggregateStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata := r.List()
	type result struct {
		status     Status
		diagnostic *Diagnostic
	}
	results := make([]result, len(metadata))
	var group sync.WaitGroup
	for index, item := range metadata {
		group.Add(1)
		go func(index int, item Metadata) {
			defer group.Done()
			status, err := r.Detect(ctx, item.ID)
			results[index].status = status
			if err != nil {
				diagnostic := Diagnostic{
					AgentID:     item.ID,
					Code:        "agent.detect-failed",
					Severity:    SeverityError,
					Summary:     "Agent detection failed",
					Detail:      err.Error(),
					Remediation: "Check the agent installation and exported diagnostics, then try again.",
					CreatedAt:   r.clock(),
				}
				results[index].diagnostic = &diagnostic
			}
		}(index, item)
	}
	group.Wait()
	aggregate := AggregateStatus{
		GeneratedAt: r.clock(),
		Agents:      make([]Status, 0, len(results)),
	}
	for _, result := range results {
		aggregate.Agents = append(aggregate.Agents, cloneStatus(result.status))
		if result.diagnostic != nil {
			aggregate.Diagnostics = append(aggregate.Diagnostics, cloneDiagnostic(*result.diagnostic))
		}
	}
	aggregate.State = aggregateState(aggregate.Agents)
	return aggregate
}

// Diagnose runs detection plus optional target-specific diagnostics. An
// unbound profile produces a clear informational diagnostic instead of trying
// to inspect a guessed installation path.
func (r *Registry) Diagnose(ctx context.Context, id ID) ([]Diagnostic, error) {
	metadata, adapter, exists := r.lookup(id)
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	status, detectErr := r.Detect(ctx, id)
	diagnostics := make([]Diagnostic, 0, 2)
	if status.State == StateUnbound {
		diagnostics = append(diagnostics, Diagnostic{
			AgentID:     metadata.ID,
			Code:        "agent.adapter-unbound",
			Severity:    SeverityInfo,
			Summary:     "No runtime adapter is connected",
			Detail:      "The public agent profile is available, but no platform-specific implementation is registered.",
			Remediation: "Bind a verified adapter before running setup or diagnostics.",
			CreatedAt:   r.clock(),
		})
	}
	if detectErr != nil {
		diagnostics = append(diagnostics, Diagnostic{
			AgentID:     metadata.ID,
			Code:        "agent.detect-failed",
			Severity:    SeverityError,
			Summary:     "Agent detection failed",
			Detail:      detectErr.Error(),
			Remediation: "Check the agent installation and exported diagnostics, then try again.",
			CreatedAt:   r.clock(),
		})
	}
	provider, supported := adapter.(DiagnosticProvider)
	if !supported || isNilAdapter(adapter) {
		if status.State != StateUnbound {
			diagnostics = append(diagnostics, Diagnostic{
				AgentID:   metadata.ID,
				Code:      "agent.diagnostics-unavailable",
				Severity:  SeverityInfo,
				Summary:   "Target-specific diagnostics are unavailable",
				Detail:    "The bound adapter does not implement diagnostics.",
				CreatedAt: r.clock(),
			})
		}
		return diagnostics, detectErr
	}
	extra, err := provider.Diagnose(ctx)
	for _, diagnostic := range extra {
		if diagnostic.AgentID == "" {
			diagnostic.AgentID = metadata.ID
		}
		if diagnostic.CreatedAt.IsZero() {
			diagnostic.CreatedAt = r.clock()
		}
		diagnostics = append(diagnostics, cloneDiagnostic(diagnostic))
	}
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			AgentID:   metadata.ID,
			Code:      "agent.diagnostics-failed",
			Severity:  SeverityError,
			Summary:   "Target-specific diagnostics failed",
			Detail:    err.Error(),
			CreatedAt: r.clock(),
		})
		if detectErr == nil {
			detectErr = err
		}
	}
	return diagnostics, detectErr
}

// DiagnoseAll aggregates diagnostic output for every registered profile.
func (r *Registry) DiagnoseAll(ctx context.Context) []Diagnostic {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata := r.List()
	diagnostics := make([]Diagnostic, 0, len(metadata))
	for _, item := range metadata {
		result, _ := r.Diagnose(ctx, item.ID)
		diagnostics = append(diagnostics, result...)
	}
	return diagnostics
}

// Backup delegates to a bound backup provider. The registry never guesses
// backup locations or copies agent data on behalf of an adapter.
func (r *Registry) Backup(ctx context.Context, id ID, request BackupRequest) (Backup, error) {
	metadata, adapter, exists := r.lookup(id)
	if !exists {
		return Backup{}, fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	if isNilAdapter(adapter) {
		return Backup{}, fmt.Errorf("%w: %s", ErrAdapterUnbound, id)
	}
	provider, supported := adapter.(BackupProvider)
	if !supported {
		return Backup{}, fmt.Errorf("%w: backup for %s", ErrCapabilityUnavailable, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	backup, err := provider.Backup(ctx, request)
	if err != nil {
		return Backup{}, err
	}
	if backup.AgentID == "" {
		backup.AgentID = metadata.ID
	}
	if backup.AgentID != metadata.ID {
		return Backup{}, fmt.Errorf("backup returned agent %q, expected %q", backup.AgentID, metadata.ID)
	}
	if backup.CreatedAt.IsZero() {
		backup.CreatedAt = r.clock()
	}
	return cloneBackup(backup), nil
}

// Perform delegates an explicit operation to a provider. It is deliberately
// unavailable until a platform-specific adapter implements OperationProvider.
func (r *Registry) Perform(ctx context.Context, id ID, request OperationRequest) (OperationResult, error) {
	metadata, adapter, exists := r.lookup(id)
	if !exists {
		return OperationResult{}, fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	if isNilAdapter(adapter) {
		return OperationResult{}, fmt.Errorf("%w: %s", ErrAdapterUnbound, id)
	}
	if request.Operation == "" {
		return OperationResult{}, errors.New("agent operation is required")
	}
	provider, supported := adapter.(OperationProvider)
	if !supported {
		return OperationResult{}, fmt.Errorf("%w: operation %q for %s", ErrCapabilityUnavailable, request.Operation, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := provider.Perform(ctx, request)
	if err != nil {
		return OperationResult{}, err
	}
	if result.AgentID == "" {
		result.AgentID = metadata.ID
	}
	if result.AgentID != metadata.ID {
		return OperationResult{}, fmt.Errorf("operation returned agent %q, expected %q", result.AgentID, metadata.ID)
	}
	if result.Operation == "" {
		result.Operation = request.Operation
	}
	return result, nil
}

func (r *Registry) lookup(id ID) (Metadata, Adapter, bool) {
	r.ensureInitialized()
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.entries[id]
	if !exists {
		return Metadata{}, nil, false
	}
	return entry.metadata.Clone(), entry.adapter, true
}

func (r *Registry) ensureInitialized() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[ID]registryEntry)
	}
	if r.now == nil {
		r.now = time.Now
	}
}

func (r *Registry) clock() time.Time {
	r.ensureInitialized()
	r.mu.RLock()
	now := r.now
	r.mu.RUnlock()
	return now().UTC()
}

func mergeMetadata(base, override Metadata) Metadata {
	merged := base.Clone()
	if override.DisplayName != "" {
		merged.DisplayName = override.DisplayName
	}
	if override.Vendor != "" {
		merged.Vendor = override.Vendor
	}
	if override.Category != "" {
		merged.Category = override.Category
	}
	if override.Description != "" {
		merged.Description = override.Description
	}
	merged.Capabilities = mergeCapabilityDeclarations(base.Capabilities, override.Capabilities)
	return merged
}

// Built-in profiles remain authoritative for their declared matrix. A binding
// may add a new capability or improve a missing summary, but it cannot turn a
// known not-implemented capability into an available one. Runtime availability
// belongs in Status.Capabilities after live detection.
func mergeCapabilityDeclarations(base, override []CapabilityDeclaration) []CapabilityDeclaration {
	if len(base) == 0 {
		return append([]CapabilityDeclaration(nil), override...)
	}
	overrides := make(map[Capability]CapabilityDeclaration, len(override))
	for _, declaration := range override {
		overrides[declaration.Capability] = declaration
	}
	merged := make([]CapabilityDeclaration, 0, len(base)+len(override))
	for _, declaration := range base {
		if incoming, exists := overrides[declaration.Capability]; exists && declaration.Summary == "" && incoming.Summary != "" {
			declaration.Summary = incoming.Summary
		}
		merged = append(merged, declaration)
		delete(overrides, declaration.Capability)
	}
	for _, declaration := range override {
		if _, remains := overrides[declaration.Capability]; remains {
			merged = append(merged, declaration)
			delete(overrides, declaration.Capability)
		}
	}
	return merged
}

func unboundStatus(metadata Metadata, now time.Time) Status {
	status := Status{
		AgentID:     metadata.ID,
		DisplayName: metadata.DisplayName,
		State:       StateUnbound,
		Message:     "No runtime adapter is connected.",
		UpdatedAt:   now,
	}
	status.Capabilities = declaredCapabilityStatuses(metadata, "No runtime adapter is connected.")
	return status
}

func errorStatus(metadata Metadata, partial Status, err error, now time.Time) Status {
	status := normalizeStatus(metadata, partial, now)
	status.State = StateError
	status.Message = err.Error()
	return status
}

func normalizeStatus(metadata Metadata, input Status, now time.Time) Status {
	status := cloneStatus(input)
	status.AgentID = metadata.ID
	if status.DisplayName == "" {
		status.DisplayName = metadata.DisplayName
	}
	if status.State == "" {
		status.State = StateUnknown
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = now
	} else {
		status.UpdatedAt = status.UpdatedAt.UTC()
	}
	status.Capabilities = mergeCapabilityStatuses(metadata, status.Capabilities)
	return status
}

func declaredCapabilityStatuses(metadata Metadata, reason string) []CapabilityStatus {
	statuses := make([]CapabilityStatus, 0, len(metadata.Capabilities))
	for _, declaration := range metadata.Capabilities {
		statuses = append(statuses, CapabilityStatus{
			Capability:   declaration.Capability,
			Availability: declaration.Availability,
			Available:    false,
			Reason:       reason,
		})
	}
	return statuses
}

func mergeCapabilityStatuses(metadata Metadata, reported []CapabilityStatus) []CapabilityStatus {
	byCapability := make(map[Capability]CapabilityStatus, len(reported))
	for _, capability := range reported {
		byCapability[capability.Capability] = capability
	}
	statuses := make([]CapabilityStatus, 0, len(metadata.Capabilities)+len(reported))
	for _, declaration := range metadata.Capabilities {
		capability, exists := byCapability[declaration.Capability]
		if !exists {
			capability = CapabilityStatus{
				Capability:   declaration.Capability,
				Availability: declaration.Availability,
				Available:    false,
				Reason:       "The adapter did not verify this capability.",
			}
		} else if capability.Availability == "" {
			capability.Availability = declaration.Availability
		}
		statuses = append(statuses, capability)
		delete(byCapability, declaration.Capability)
	}
	for _, capability := range reported {
		if _, remains := byCapability[capability.Capability]; remains {
			statuses = append(statuses, capability)
			delete(byCapability, capability.Capability)
		}
	}
	return statuses
}

func aggregateState(statuses []Status) State {
	if len(statuses) == 0 {
		return StateUnknown
	}
	allReady := true
	allUnbound := true
	allNotInstalled := true
	hasDetected := false
	for _, status := range statuses {
		switch status.State {
		case StateError, StateDegraded:
			return StateDegraded
		case StateReady:
			allUnbound = false
			allNotInstalled = false
		case StateUnbound:
			allReady = false
			allNotInstalled = false
		case StateNotInstalled:
			allReady = false
			allUnbound = false
		case StateDetected:
			allReady = false
			allUnbound = false
			allNotInstalled = false
			hasDetected = true
		default:
			allReady = false
			allUnbound = false
			allNotInstalled = false
		}
	}
	if allReady {
		return StateReady
	}
	if allUnbound {
		return StateUnbound
	}
	if allNotInstalled {
		return StateNotInstalled
	}
	if hasDetected {
		return StateDetected
	}
	return StateUnknown
}

func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
