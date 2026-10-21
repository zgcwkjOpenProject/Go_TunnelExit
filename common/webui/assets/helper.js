// 把字节数格式化成便于阅读的单位
function formatBytes(value) {
  var size = Number(value) || 0;
  if (size < 1024) { return size + ' B'; }
  var units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  var index = -1;
  do {
    size = size / 1024;
    index += 1;
  } while (size >= 1024 && index < units.length - 1);
  return size.toFixed(size >= 100 ? 0 : 1) + ' ' + units[index];
}

// 生成状态徽标
function stateBadge(state) {
  var badge = document.createElement('span');
  if (state === 'online') {
    badge.className = 'badge on';
    badge.textContent = '在线';
  } else if (state === 'establishing') {
    badge.className = 'badge warn';
    badge.textContent = '建立中';
  } else {
    badge.className = 'badge off';
    badge.textContent = '离线';
  }
  return badge;
}