package team

// Scheduler selects teammates for ready tasks.
type Scheduler interface {
	Select(teammates []Teammate, tasks []Task) []Assignment
}

// RoundRobinScheduler assigns tasks to teammates in round-robin order.
type RoundRobinScheduler struct {
	next int
}

// Select returns assignments for the provided tasks.
func (s *RoundRobinScheduler) Select(teammates []Teammate, tasks []Task) []Assignment {
	if len(teammates) == 0 || len(tasks) == 0 {
		return nil
	}
	assignments := make([]Assignment, 0, len(tasks))
	index := s.next
	for _, task := range tasks {
		teammate := teammates[index%len(teammates)]
		assignments = append(assignments, Assignment{Task: task, Teammate: teammate})
		index++
	}
	s.next = index % len(teammates)
	return assignments
}
