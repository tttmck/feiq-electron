// Package ipmsg 实现 IP Messenger (飞鸽传书) 通信协议，与飞秋等兼容客户端互通。
// 命令字定义参照协议第 9 版草案，数值与 iptux/FeiQ 保持一致。
package ipmsg

// 命令字低 8 位：命令功能
const (
	CmdNoOperation    uint32 = 0x00 // 无操作
	CmdBrEntry        uint32 = 0x01 // 上线广播
	CmdBrExit         uint32 = 0x02 // 下线广播
	CmdAnsEntry       uint32 = 0x03 // 上线应答
	CmdBrAbsence      uint32 = 0x04 // 离开/状态变更广播

	CmdBrIsGetList  uint32 = 0x10 // 搜寻可提供主机列表的成员
	CmdOkGetList    uint32 = 0x11 // 通知可提供主机列表
	CmdGetList      uint32 = 0x12 // 请求主机列表
	CmdAnsList      uint32 = 0x13 // 应答主机列表
	CmdBrIsGetList2 uint32 = 0x18 // 搜寻主机列表(2)

	CmdSendMsg    uint32 = 0x20 // 发送消息
	CmdRecvMsg    uint32 = 0x21 // 消息送达确认
	CmdReadMsg    uint32 = 0x30 // 消息已读通知
	CmdDelMsg     uint32 = 0x31 // 消息丢弃通知
	CmdAnsReadMsg uint32 = 0x32 // 已读确认

	CmdGetInfo  uint32 = 0x40 // 获取版本信息
	CmdSendInfo uint32 = 0x41 // 应答版本信息

	CmdGetAbsenceInfo  uint32 = 0x50 // 获取离开状态说明
	CmdSendAbsenceInfo uint32 = 0x51 // 应答离开状态说明

	CmdGetFileData   uint32 = 0x60 // TCP 请求文件数据
	CmdReleaseFiles  uint32 = 0x61 // 取消附件接收
	CmdGetDirFiles   uint32 = 0x62 // TCP 请求目录数据
	CmdGetPubKey     uint32 = 0x70 // 请求 RSA 公钥
	CmdAnsPubKey     uint32 = 0x71 // 应答 RSA 公钥
)

// 命令字高 24 位：选项标志
const (
	OptAbsence     uint32 = 0x00000100 // 离开状态（成员识别用）
	OptServer      uint32 = 0x00000200
	OptSendCheck   uint32 = 0x00000100 // 消息需要送达确认
	OptSecret      uint32 = 0x00000200 // 密封消息
	OptBroadcast   uint32 = 0x00000400 // 广播消息
	OptMulticast   uint32 = 0x00000800 // 多播
	OptNoPopup     uint32 = 0x00001000
	OptAutoRet     uint32 = 0x00002000 // 自动回复
	OptRetry       uint32 = 0x00004000 // 重发
	OptPassword    uint32 = 0x00008000
	OptDialUp      uint32 = 0x00010000
	OptNoLog       uint32 = 0x00020000 // 不写日志
	OptNewMuti     uint32 = 0x00040000
	OptNoAddList   uint32 = 0x00080000 // 不加入好友列表
	OptReadCheck   uint32 = 0x00100000 // 已读确认
	OptFileAttach  uint32 = 0x00200000 // 消息带文件附件
	OptEncrypt     uint32 = 0x00400000 // 加密
	OptUTF8        uint32 = 0x00800000 // 附加段为 UTF-8 编码
	OptSecretEx    uint32 = OptReadCheck | OptSecret
)

// 附件文件类型（fileattr 低 8 位）
const (
	FileRegular   uint32 = 0x01 // 普通文件
	FileDir       uint32 = 0x02 // 目录
	FileRetParent uint32 = 0x03 // 目录流中返回上级
)

// 附件扩展属性类型
const (
	FileExtUnicodeFname uint32 = 0x41 // Unicode 文件名（UTF-8）
)

// Mode 返回命令字的功能部分（低 8 位）。
func Mode(c uint32) uint32 { return c & 0x000000ff }

// Opt 返回命令字的选项部分（高 24 位）。
func Opt(c uint32) uint32 { return c & 0xffffff00 }

// CommandName 返回命令功能的可读名称（日志/调试用）。
func CommandName(c uint32) string {
	switch Mode(c) {
	case CmdNoOperation:
		return "NOOPERATION"
	case CmdBrEntry:
		return "BR_ENTRY"
	case CmdBrExit:
		return "BR_EXIT"
	case CmdAnsEntry:
		return "ANSENTRY"
	case CmdBrAbsence:
		return "BR_ABSENCE"
	case CmdSendMsg:
		return "SENDMSG"
	case CmdRecvMsg:
		return "RECVMSG"
	case CmdReadMsg:
		return "READMSG"
	case CmdGetInfo:
		return "GETINFO"
	case CmdSendInfo:
		return "SENDINFO"
	case CmdGetFileData:
		return "GETFILEDATA"
	case CmdReleaseFiles:
		return "RELEASEFILES"
	case CmdGetDirFiles:
		return "GETDIRFILES"
	default:
		return "UNKNOWN"
	}
}
