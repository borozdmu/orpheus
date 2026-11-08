package agentcontract

import "errors"

// Audience describes current recipients at the time of a sensitive operation.
// Later publication/history membership changes remain an administrator's action.
type AudienceGroup struct {
	Instance string `json:"instance"`
	TeamID   string `json:"team_id"`
	Revision string `json:"revision"`
}
type Audience struct {
	Kind        string          `json:"kind"`
	Instance    string          `json:"instance"`
	ChannelID   string          `json:"channel_id"`
	ChannelType string          `json:"channel_type"`
	Complete    bool            `json:"complete"`
	Groups      []AudienceGroup `json:"groups"`
}

func (a Audience) Validate() error {
	denied := errors.New("current audience coverage is not proven")
	if !a.Complete || !plain(a.Instance) || !plain(a.ChannelID) {
		return denied
	}
	switch a.Kind {
	case "exact":
		if a.ChannelType != "P" && a.ChannelType != "D" && a.ChannelType != "telegram-managed" && a.ChannelType != "telegram-private" {
			return denied
		}
		if len(a.Groups) != 0 {
			return denied
		}
	case "team-current":
		if a.ChannelType != "O" || len(a.Groups) == 0 {
			return denied
		}
		seen := map[string]bool{}
		for _, g := range a.Groups {
			id := g.Instance + ":" + g.TeamID
			if !plain(g.Instance) || !plain(g.TeamID) || !plain(g.Revision) || seen[id] {
				return denied
			}
			seen[id] = true
		}
	default:
		return denied
	}
	return nil
}
