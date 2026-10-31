// status 定义服务端、客户端与 Web 界面共用的节点展示结构
//
// 放在 app 包是为了让 webui 既能读取又不反向依赖 server
package app

import "time"

// NodeState 是节点在展示页面上的状态
type NodeState string

const (
	NodeStateEstablishing NodeState = "establishing" // 已登录但空闲连接池未就绪，暂时不能出网
	NodeStateOnline       NodeState = "online"       // 空闲连接池可用，可以出网
	NodeStateOffline      NodeState = "offline"      // 只保留历史端口映射，当前没有客户端会话
)

// NodeStatus 是一个客户端节点的运行态数据，由服务端汇总后交给节点展示页
type NodeStatus struct {
	ID            string    `json:"id"`            // 客户端唯一标识，服务端按它加载端口分配记录
	Address       string    `json:"address"`       // 客户端出口 IP，与 ID 一起决定 SOCKS5 端口分配起点
	Name          string    `json:"name"`          // 客户端显示名，只用于服务端日志与节点记录
	Port          int       `json:"port"`          // 分配给该客户端的 SOCKS5 端口
	State         NodeState `json:"state"`         // 当前状态
	Place         string    `json:"place"`         // 地图落点键，与展示页底图的落点表同名，空串表示无法定位
	Region        string    `json:"region"`        // 出口 IP 的归属地展示文本
	Up            uint64    `json:"up"`            // 累计上行字节数，外部客户端发往目标方向
	Down          uint64    `json:"down"`          // 累计下行字节数，目标返回给外部客户端方向
	ActiveStreams int       `json:"activeStreams"` // 当前正在转发的流数量
	TotalStreams  uint64    `json:"totalStreams"`  // 累计建立过的流数量，仅进程运行期间统计
	LastSeen      time.Time `json:"lastSeen"`      // 最近一次确认在线的时间，零值表示从未在线
}
