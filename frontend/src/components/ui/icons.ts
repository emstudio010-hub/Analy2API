// 图标名与 24x24 统一描边路径数据。
//
// 单独放一个 .ts 文件：`<script setup>` 不允许导出 ES 模块类型成员，
// 放到这里后各视图与布局组件可安全 `import type { IconName }`。

export type IconName =
  | 'activity'
  | 'alert'
  | 'alertCircle'
  | 'arrowUpRight'
  | 'box'
  | 'check'
  | 'checkCircle'
  | 'chevronDown'
  | 'chevronLeft'
  | 'chevronRight'
  | 'clock'
  | 'close'
  | 'code'
  | 'coins'
  | 'copy'
  | 'cpu'
  | 'dashboard'
  | 'database'
  | 'download'
  | 'edit'
  | 'external'
  | 'eye'
  | 'eyeOff'
  | 'fileText'
  | 'filter'
  | 'folder'
  | 'gauge'
  | 'gift'
  | 'globe'
  | 'helpCircle'
  | 'inbox'
  | 'info'
  | 'key'
  | 'layers'
  | 'link'
  | 'lock'
  | 'logout'
  | 'menu'
  | 'moon'
  | 'network'
  | 'panelLeft'
  | 'pause'
  | 'play'
  | 'plus'
  | 'refresh'
  | 'rotate'
  | 'scan'
  | 'search'
  | 'server'
  | 'settings'
  | 'shield'
  | 'shieldKey'
  | 'sliders'
  | 'sparkles'
  | 'stop'
  | 'sun'
  | 'tag'
  | 'terminal'
  | 'trash'
  | 'upload'
  | 'users'
  | 'wrapText'
  | 'xCircle'
  | 'zap'

/** 24x24 视野内的统一描边路径。 */
export const iconPaths: Record<IconName, string> = {
  activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  alert:
    '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  alertCircle: '<circle cx="12" cy="12" r="9"/><path d="M12 8v4.5"/><path d="M12 16.5h.01"/>',
  arrowUpRight: '<path d="M7 17L17 7"/><path d="M8 7h9v9"/>',
  box: '<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="M3.3 7l8.7 5 8.7-5"/><path d="M12 22V12"/>',
  check: '<path d="M5 13l4 4L19 7"/>',
  checkCircle: '<circle cx="12" cy="12" r="9"/><path d="M8.2 12.4l2.6 2.6 5-5.2"/>',
  chevronDown: '<path d="M6 9l6 6 6-6"/>',
  chevronLeft: '<path d="M15 18l-6-6 6-6"/>',
  chevronRight: '<path d="M9 6l6 6-6 6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7.5V12l3 2"/>',
  close: '<path d="M6 6l12 12"/><path d="M18 6L6 18"/>',
  code: '<path d="M16 18l6-6-6-6"/><path d="M8 6l-6 6 6 6"/><path d="M14.5 4l-5 16"/>',
  coins:
    '<circle cx="9" cy="9" r="6"/><path d="M14.8 10.4A6 6 0 1 1 10.4 14.8"/><path d="M9 6.5v5"/><path d="M7.2 8.2h3.6"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  cpu: '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 2v3"/><path d="M15 2v3"/><path d="M9 19v3"/><path d="M15 19v3"/><path d="M2 9h3"/><path d="M2 15h3"/><path d="M19 9h3"/><path d="M19 15h3"/>',
  dashboard:
    '<rect x="3" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="3" width="7.5" height="7.5" rx="1.5"/><rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5"/><rect x="13.5" y="13.5" width="7.5" height="7.5" rx="1.5"/>',
  database:
    '<ellipse cx="12" cy="5.5" rx="8" ry="3"/><path d="M4 5.5v13c0 1.7 3.6 3 8 3s8-1.3 8-3v-13"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  download:
    '<path d="M21 15.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-3.5"/><path d="M12 15.5v-12"/><path d="M7 10.5l5 5 5-5"/>',
  edit: '<path d="M12 20h9"/><path d="M16.6 3.6a2.1 2.1 0 0 1 3 3L7.2 19 3 20l1-4.2z"/>',
  external:
    '<path d="M14 4h6v6"/><path d="M20 4l-9 9"/><path d="M18 14.5V18a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h3.5"/>',
  eye: '<path d="M2 12s3.6-6.5 10-6.5S22 12 22 12s-3.6 6.5-10 6.5S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  eyeOff:
    '<path d="M3 3l18 18"/><path d="M10.6 5.6A10 10 0 0 1 12 5.5c6.4 0 10 6.5 10 6.5a17.6 17.6 0 0 1-2.9 3.7"/><path d="M6.5 6.9A17 17 0 0 0 2 12s3.6 6.5 10 6.5c1.4 0 2.6-.3 3.7-.7"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/>',
  fileText:
    '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M8 13h8"/><path d="M8 17h6"/>',
  filter: '<path d="M3 5h18l-7 8.2V20l-4-2.2v-4.6z"/>',
  folder:
    '<path d="M3 7a2 2 0 0 1 2-2h3.6l2 2.2H19a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  gauge:
    '<path d="M12 14l3.5-3.5"/><path d="M20.3 18A9 9 0 1 0 3.7 18z"/><circle cx="12" cy="14" r="1.5"/>',
  gift: '<rect x="3" y="8" width="18" height="4" rx="1"/><path d="M12 8v13"/><path d="M19 12v7a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2v-7"/><path d="M7.5 8a2.5 2.5 0 0 1 0-5C9 3 12 8 12 8s3-5 4.5-5a2.5 2.5 0 0 1 0 5"/>',
  globe:
    '<circle cx="12" cy="12" r="9"/><path d="M3.6 9h16.8"/><path d="M3.6 15h16.8"/><path d="M12 3a14 14 0 0 1 0 18"/><path d="M12 3a14 14 0 0 0 0 18"/>',
  helpCircle:
    '<circle cx="12" cy="12" r="9"/><path d="M9.1 9a3 3 0 0 1 5.8 1c0 2-3 2.5-3 4"/><path d="M12 17.2h.01"/>',
  inbox:
    '<polyline points="22 12 16 12 14 15 10 15 8 12 2 12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5.5"/><path d="M12 7.5h.01"/>',
  key: '<circle cx="7.5" cy="16.5" r="4"/><path d="M10.4 13.6 20 4"/><path d="M16.8 7.2l2.6 2.6"/>',
  layers:
    '<path d="M12 3l9 4.8-9 4.8-9-4.8z"/><path d="M3 12.5l9 4.8 9-4.8"/><path d="M3 17l9 4.8 9-4.8"/>',
  link: '<path d="M10 13a5 5 0 0 0 7.1 0l2.8-2.8a5 5 0 0 0-7.1-7.1L11.2 4.7"/><path d="M14 11a5 5 0 0 0-7.1 0l-2.8 2.8a5 5 0 0 0 7.1 7.1l1.6-1.6"/>',
  lock: '<rect x="3.5" y="10.5" width="17" height="10.5" rx="2"/><path d="M7.5 10.5V7a4.5 4.5 0 0 1 9 0v3.5"/>',
  logout:
    '<path d="M9.5 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4.5"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/>',
  menu: '<path d="M3 6h18"/><path d="M3 12h18"/><path d="M3 18h18"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
  network:
    '<rect x="9" y="2" width="6" height="5" rx="1"/><rect x="2" y="17" width="6" height="5" rx="1"/><rect x="16" y="17" width="6" height="5" rx="1"/><path d="M12 7v5"/><path d="M5 17v-3a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v3"/>',
  panelLeft: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 3v18"/>',
  pause: '<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>',
  play: '<polygon points="6 4 20 12 6 20 6 4"/>',
  plus: '<path d="M12 5v14"/><path d="M5 12h14"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-2.7-6.4"/><path d="M21 4v5h-5"/>',
  rotate:
    '<path d="M3 12a9 9 0 0 1 15.5-6.3L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15.5 6.3L3 16"/><path d="M3 21v-5h5"/>',
  scan: '<path d="M3 7V5a2 2 0 0 1 2-2h2"/><path d="M17 3h2a2 2 0 0 1 2 2v2"/><path d="M21 17v2a2 2 0 0 1-2 2h-2"/><path d="M7 21H5a2 2 0 0 1-2-2v-2"/><path d="M7 12h10"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.6-3.6"/>',
  server:
    '<rect x="2.5" y="3.5" width="19" height="7" rx="1.8"/><rect x="2.5" y="13.5" width="19" height="7" rx="1.8"/><path d="M6.5 7h.01"/><path d="M6.5 17h.01"/><path d="M11 7h6"/><path d="M11 17h6"/>',
  settings:
    '<path d="M4 7h6"/><path d="M14 7h6"/><path d="M4 17h6"/><path d="M14 17h6"/><circle cx="12" cy="7" r="2.2"/><circle cx="9" cy="17" r="2.2"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
  shieldKey:
    '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><circle cx="12" cy="10.5" r="2"/><path d="M12 12.5v3.5"/><path d="M12 14.5h1.8"/>',
  sliders:
    '<path d="M4 21v-7"/><path d="M4 10V3"/><path d="M12 21v-9"/><path d="M12 8V3"/><path d="M20 21v-5"/><path d="M20 12V3"/><path d="M2 14h4"/><path d="M10 8h4"/><path d="M18 16h4"/>',
  sparkles:
    '<path d="M12 3l1.9 4.6L18.5 9.5l-4.6 1.9L12 16l-1.9-4.6L5.5 9.5l4.6-1.9z"/><path d="M19 15l.9 2.1L22 18l-2.1.9L19 21l-.9-2.1L16 18l2.1-.9z"/>',
  stop: '<rect x="5" y="5" width="14" height="14" rx="2"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="M4.9 4.9l1.4 1.4"/><path d="M17.7 17.7l1.4 1.4"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="M4.9 19.1l1.4-1.4"/><path d="M17.7 6.3l1.4-1.4"/>',
  tag: '<path d="M20.6 13.4l-7.2 7.2a2 2 0 0 1-2.8 0L3 13V3h10l7.6 7.6a2 2 0 0 1 0 2.8z"/><circle cx="7.5" cy="7.5" r="1.2"/>',
  terminal: '<path d="M4 17l6-6-6-6"/><path d="M12 19h8"/>',
  trash:
    '<path d="M3 6h18"/><path d="M8 6V4.2A1.2 1.2 0 0 1 9.2 3h5.6A1.2 1.2 0 0 1 16 4.2V6"/><path d="M19 6l-1 14a2 2 0 0 1-2 1.8H8A2 2 0 0 1 6 20L5 6"/><path d="M10 11v6"/><path d="M14 11v6"/>',
  upload:
    '<path d="M21 15.5V19a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-3.5"/><path d="M12 3.5v13"/><path d="M7 8.5l5-5 5 5"/>',
  users:
    '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M16.2 5.3a3.5 3.5 0 0 1 0 6.6"/><path d="M18 20a6.5 6.5 0 0 0-1.9-4.6"/>',
  wrapText:
    '<path d="M3 6h18"/><path d="M3 12h15a3 3 0 1 1 0 6h-4"/><polyline points="16 16 14 18 16 20"/><path d="M3 18h7"/>',
  xCircle: '<circle cx="12" cy="12" r="9"/><path d="M15 9l-6 6"/><path d="M9 9l6 6"/>',
  zap: '<path d="M13 2.5 4.5 14H11l-.8 7.5L19 10h-6.4z"/>',
}
