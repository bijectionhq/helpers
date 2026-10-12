package landing

import "testing"

func TestStatusRejectsInconsistentProtocol(t *testing.T) {
	for _, state := range []State{Unpublished, Pending, Unconfirmed, Published, Abandoned, Conflict} {
		t.Run(string(state), func(t *testing.T) {
			s := PublicationStatus{Format: 1, Kind: "land-publication", ID: "identity", State: state, Receipt: "receipt"}
			if state == Published {
				s.Commit = "commit"
			}
			if state == Conflict {
				s.Conflict = "resolution"
			}
			if err := s.validate(s.ID); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*PublicationStatus){
				func(s *PublicationStatus) { s.Format++ },
				func(s *PublicationStatus) { s.ID = "other" },
				func(s *PublicationStatus) { s.State = "invented" },
				func(s *PublicationStatus) {
					if s.Commit == "" {
						s.Commit = "unexpected"
					} else {
						s.Commit = ""
					}
				},
				func(s *PublicationStatus) {
					if s.Conflict == "" {
						s.Conflict = "unexpected"
					} else {
						s.Conflict = ""
					}
				},
			} {
				invalid := s
				mutate(&invalid)
				if err := invalid.validate(s.ID); err == nil {
					t.Fatal("invalid state accepted", invalid)
				}
			}
		})
	}
}
