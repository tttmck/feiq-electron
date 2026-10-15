// 渲染进程桥：只暴露最小 API 面
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('feiq', {
  // 调 Go 后端 RPC；返回 {id, result?, error?}
  rpc: (method, params) => ipcRenderer.invoke('rpc', method, params),
  // 原生文件选择，返回路径数组
  pickFiles: () => ipcRenderer.invoke('pick-files'),
  // 接收文件确认，返回 true/false
  confirmOffer: (summary) => ipcRenderer.invoke('confirm-offer', summary),
  // 后端事件订阅
  onEvent: (handler) => {
    ipcRenderer.on('backend-event', (_ev, name, data) => handler(name, data));
  },
});
