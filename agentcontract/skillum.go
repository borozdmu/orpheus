package agentcontract

import "github.com/google/uuid"

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

// The connector derives the source from the authenticated workspace and
// reconfirms membership and agent admission before each protected operation.
func (a Assignment) validSkillumDialog() bool {
	namespace := "skillum:" + a.Delivery.Source
	return a.Delivery.Connector == "skillum" && a.Delivery.Thread == "" &&
		canonicalID(a.Delivery.Source) && canonicalID(a.Delivery.Chat) && canonicalID(a.Delivery.Message) &&
		a.Audience.Kind == "exact" && a.Audience.ChannelType == "skillum-private" &&
		a.Audience.Instance == namespace && a.Author.Namespace == namespace &&
		a.Author.BotID == "" && len(a.Readers) == 1 && a.Readers[0] == a.Author &&
		len(a.Attachments) == 0
}
