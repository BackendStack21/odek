package loop

const (
	reassessmentRepeatedCheckFailure = "repeated_check_failure"
	reassessmentVariedToolFailures   = "varied_tool_failures"
	maxReassessmentChecks            = 64
	maxReassessmentClasses           = 3
	maxReassessmentFingerprints      = 8
	maxReassessmentHints             = 2
)

type reassessmentObservation struct {
	fingerprint string
	errorClass  string
	failed      bool
	check       bool
}

type reassessmentCheckState struct {
	lastBatch int
	streak    int
}

type reassessmentClassState struct {
	lastSerial   int
	streak       int
	fingerprints map[string]struct{}
	order        []string
}

type reassessmentMonitor struct {
	checks      map[string]reassessmentCheckState
	checkOrder  []string
	classes     map[string]*reassessmentClassState
	classOrder  []string
	lastHint    int
	hints       int
	initialized bool
	serial      int
}

func (m *reassessmentMonitor) observe(batch int, observations []reassessmentObservation) string {
	if len(observations) == 0 {
		return ""
	}
	if m.checks == nil {
		m.checks = make(map[string]reassessmentCheckState)
	}
	if m.classes == nil {
		m.classes = make(map[string]*reassessmentClassState)
	}
	// Collapse each fingerprint to its last observation in this batch. This
	// makes parallel duplicate failures contribute at most once and gives a
	// later result in the batch authoritative status.
	last := make(map[string]reassessmentObservation)
	order := make([]string, 0, len(observations))
	for _, obs := range observations {
		if obs.fingerprint == "" {
			continue
		}
		if _, ok := last[obs.fingerprint]; !ok {
			order = append(order, obs.fingerprint)
		}
		last[obs.fingerprint] = obs
	}
	if len(last) == 0 {
		return ""
	}
	m.serial++
	var repeated bool
	var successful bool
	failedClasses := make(map[string]map[string]struct{})
	failedClassOrder := make(map[string][]string)
	for _, fingerprint := range order {
		obs := last[fingerprint]
		if !obs.failed {
			successful = true
			if obs.check {
				delete(m.checks, fingerprint)
				m.removeCheck(fingerprint)
			}
			continue
		}
		if obs.check {
			state := m.checks[fingerprint]
			if state.lastBatch == batch {
				// Already collapsed, retained for clarity if callers reuse state.
			} else if !m.initialized || state.lastBatch < batch {
				state.streak++
			} else {
				state.streak = 1
			}
			state.lastBatch = batch
			if _, exists := m.checks[fingerprint]; !exists {
				if len(m.checkOrder) >= maxReassessmentChecks {
					delete(m.checks, m.checkOrder[0])
					m.checkOrder = m.checkOrder[1:]
				}
				m.checkOrder = append(m.checkOrder, fingerprint)
			}
			m.checks[fingerprint] = state
			if state.streak >= 3 {
				repeated = true
			}
		}
		if obs.errorClass != "" {
			if failedClasses[obs.errorClass] == nil {
				failedClasses[obs.errorClass] = make(map[string]struct{})
			}
			if _, exists := failedClasses[obs.errorClass][fingerprint]; !exists {
				failedClassOrder[obs.errorClass] = append(failedClassOrder[obs.errorClass], fingerprint)
			}
			failedClasses[obs.errorClass][fingerprint] = struct{}{}
		}
	}
	if successful {
		m.classes = make(map[string]*reassessmentClassState)
		m.classOrder = nil
		failedClasses = nil
	}
	classNames := make([]string, 0, len(failedClasses))
	if !successful {
		for _, fingerprint := range order {
			obs := last[fingerprint]
			if !obs.failed || obs.errorClass == "" {
				continue
			}
			seen := false
			for _, class := range classNames {
				if class == obs.errorClass {
					seen = true
					break
				}
			}
			if !seen {
				classNames = append(classNames, obs.errorClass)
			}
		}
	}
	for _, class := range classNames {
		state := m.classes[class]
		if state == nil {
			if len(m.classOrder) >= maxReassessmentClasses {
				old := m.classOrder[0]
				delete(m.classes, old)
				m.classOrder = m.classOrder[1:]
			}
			state = &reassessmentClassState{lastSerial: m.serial - 1, fingerprints: make(map[string]struct{})}
			m.classes[class] = state
			m.classOrder = append(m.classOrder, class)
		}
		if state.lastSerial < m.serial {
			if state.lastSerial == m.serial-1 {
				state.streak++
			} else {
				state.streak = 1
				state.fingerprints = make(map[string]struct{})
				state.order = nil
			}
			state.lastSerial = m.serial
		}
		for _, fingerprint := range failedClassOrder[class] {
			if _, ok := state.fingerprints[fingerprint]; ok {
				continue
			}
			if len(state.order) >= maxReassessmentFingerprints {
				old := state.order[0]
				delete(state.fingerprints, old)
				state.order = state.order[1:]
			}
			state.fingerprints[fingerprint] = struct{}{}
			state.order = append(state.order, fingerprint)
		}
	}
	m.initialized = true
	if repeated {
		return m.hint(batch, reassessmentRepeatedCheckFailure)
	}
	for _, class := range classNames {
		state := m.classes[class]
		if state != nil && state.lastSerial == m.serial && state.streak >= 3 && len(state.fingerprints) >= 2 {
			return m.hint(batch, reassessmentVariedToolFailures)
		}
	}
	return ""
}

func (m *reassessmentMonitor) removeCheck(fingerprint string) {
	for i, value := range m.checkOrder {
		if value == fingerprint {
			m.checkOrder = append(m.checkOrder[:i], m.checkOrder[i+1:]...)
			return
		}
	}
}

func (m *reassessmentMonitor) hint(batch int, reason string) string {
	if m.hints >= maxReassessmentHints {
		return ""
	}
	if m.hints > 0 && batch-m.lastHint < 3 {
		return ""
	}
	m.hints++
	m.lastHint = batch
	return reason
}
