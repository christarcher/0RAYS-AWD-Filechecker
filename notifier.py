#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import argparse
import json
import logging
import threading
import time
from datetime import datetime
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse
import sys
import os

# 跨平台通知支持
try:
    from plyer import notification
    PLYER_AVAILABLE = True
except ImportError:
    PLYER_AVAILABLE = False
    print("警告: plyer库未安装，将使用系统原生通知方式")
    print("安装命令: pip install plyer")

# 系统原生通知方式
import platform
import subprocess

# 配置日志
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(levelname)s - %(message)s',
    handlers=[
        logging.StreamHandler(sys.stdout),
        logging.FileHandler('edr_notifier.log', encoding='utf-8')
    ]
)

logger = logging.getLogger(__name__)

class EDRNotifier:
    def __init__(self, sound_enabled=True, log_to_file=True):
        self.sound_enabled = sound_enabled
        self.alert_count = 0
        self.system = platform.system()
        logger.info(f"EDR告警提醒器启动 - 系统: {self.system}")
        
    def send_notification(self, title, message, alert_type="info"):
        """发送系统通知"""
        try:
            if PLYER_AVAILABLE:
                # 使用plyer库 (推荐，跨平台支持)
                icon_path = self._get_icon_path(alert_type)
                notification.notify(
                    title=title,
                    message=message,
                    timeout=10,
                    app_icon=icon_path
                )
                logger.info(f"通知已发送 (plyer): {title}")
            else:
                # 使用系统原生方式
                self._send_native_notification(title, message, alert_type)
                
        except Exception as e:
            logger.error(f"发送通知失败: {e}")
            # 备用方案：控制台提醒
            print(f"\n{'='*50}")
            print(f"🚨 EDR告警 🚨")
            print(f"时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
            print(f"标题: {title}")
            print(f"内容: {message}")
            print(f"类型: {alert_type}")
            print(f"{'='*50}\n")
    
    def _get_icon_path(self, alert_type):
        """根据告警类型获取图标路径"""
        # 可以自定义图标路径
        icon_map = {
            "warning": None,  # 可以添加警告图标路径
            "error": None,    # 可以添加错误图标路径  
            "info": None      # 可以添加信息图标路径
        }
        return icon_map.get(alert_type)
    
    def _send_native_notification(self, title, message, alert_type):
        """发送系统原生通知"""
        try:
            if self.system == "Windows":
                # Windows 通知
                import ctypes
                ctypes.windll.user32.MessageBoxW(0, message, title, 0x40)
                logger.info(f"通知已发送 (Windows): {title}")
                
            elif self.system == "Darwin":  # macOS
                # macOS 通知
                script = f'''
                display notification "{message}" with title "{title}"
                '''
                subprocess.run(["osascript", "-e", script], check=True)
                logger.info(f"通知已发送 (macOS): {title}")
                
            elif self.system == "Linux":
                # Linux 通知
                subprocess.run([
                    "notify-send", 
                    title, 
                    message,
                    "--urgency=critical",
                    "--expire-time=10000"
                ], check=True)
                logger.info(f"通知已发送 (Linux): {title}")
                
        except Exception as e:
            logger.error(f"系统原生通知发送失败: {e}")
    
    def play_alert_sound(self):
        """播放告警音效"""
        if not self.sound_enabled:
            return
            
        try:
            if self.system == "Windows":
                import winsound
                winsound.MessageBeep()
            elif self.system == "Darwin":  # macOS
                os.system("afplay /System/Library/Sounds/Glass.aiff")
            elif self.system == "Linux":
                os.system("paplay /usr/share/sounds/alsa/Front_Right.wav 2>/dev/null || echo -e '\a'")
        except Exception as e:
            logger.warning(f"播放告警音效失败: {e}")

class EDRAlertHandler(BaseHTTPRequestHandler):
    def __init__(self, *args, notifier=None, **kwargs):
        self.notifier = notifier
        super().__init__(*args, **kwargs)
    
    def do_GET(self):
        """处理GET请求"""
        parsed_url = urlparse(self.path)
        
        if parsed_url.path == "/api/agent/edr-alert":
            self._handle_edr_alert(parsed_url)
        elif parsed_url.path == "/health":
            self._handle_health_check()
        elif parsed_url.path == "/stats":
            self._handle_stats()
        else:
            self._send_error_response(404, "Not Found")
    
    def do_POST(self):
        """处理POST请求"""
        parsed_url = urlparse(self.path)
        
        if parsed_url.path == "/api/agent/edr-alert":
            self._handle_edr_alert_post()
        else:
            self._send_error_response(404, "Not Found")
    
    def _handle_edr_alert(self, parsed_url):
        """处理EDR告警 (GET方式)"""
        try:
            # 解析查询参数
            query_params = parse_qs(parsed_url.query)
            alert_type = query_params.get('type', ['info'])[0]
            message = query_params.get('message', ['未知告警'])[0]
            
            self._process_alert(alert_type, message)
            
            # 返回成功响应
            self._send_json_response(200, {
                "status": "success",
                "message": "告警已接收并处理"
            })
            
        except Exception as e:
            logger.error(f"处理EDR告警失败: {e}")
            self._send_error_response(500, f"处理告警失败: {str(e)}")
    
    def _handle_edr_alert_post(self):
        """处理EDR告警 (POST方式)"""
        try:
            content_length = int(self.headers.get('Content-Length', 0))
            post_data = self.rfile.read(content_length).decode('utf-8')
            
            # 解析JSON数据
            alert_data = json.loads(post_data)
            alert_type = alert_data.get('type', 'info')
            message = alert_data.get('message', '未知告警')
            
            self._process_alert(alert_type, message)
            
            # 返回成功响应
            self._send_json_response(200, {
                "status": "success",
                "message": "告警已接收并处理"
            })
            
        except Exception as e:
            logger.error(f"处理POST告警失败: {e}")
            self._send_error_response(500, f"处理告警失败: {str(e)}")
    
    def _process_alert(self, alert_type, message):
        """处理告警逻辑"""
        self.notifier.alert_count += 1
        
        # 记录告警
        logger.warning(f"EDR告警 #{self.notifier.alert_count}: [{alert_type.upper()}] {message}")
        
        # 生成通知标题
        title_map = {
            "warning": "⚠️ EDR 安全警告",
            "error": "❌ EDR 严重错误", 
            "critical": "🚨 EDR 紧急告警",
            "info": "ℹ️ EDR 信息"
        }
        
        title = title_map.get(alert_type, "📢 EDR 通知")
        
        # 发送系统通知
        self.notifier.send_notification(title, message, alert_type)
        
        # 播放告警音效
        self.notifier.play_alert_sound()
        
        # 如果是严重告警，额外处理
        if alert_type in ["error", "critical"]:
            logger.critical(f"严重告警触发: {message}")
    
    def _handle_health_check(self):
        """健康检查端点"""
        self._send_json_response(200, {
            "status": "healthy",
            "service": "EDR Alert Notifier",
            "uptime": time.time(),
            "alert_count": self.notifier.alert_count
        })
    
    def _handle_stats(self):
        """统计信息端点"""
        self._send_json_response(200, {
            "alert_count": self.notifier.alert_count,
            "system": platform.system(),
            "plyer_available": PLYER_AVAILABLE,
            "sound_enabled": self.notifier.sound_enabled
        })
    
    def _send_json_response(self, status_code, data):
        """发送JSON响应"""
        self.send_response(status_code)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Access-Control-Allow-Origin', '*')
        self.end_headers()
        
        response = json.dumps(data, ensure_ascii=False, indent=2)
        self.wfile.write(response.encode('utf-8'))
    
    def _send_error_response(self, status_code, message):
        """发送错误响应"""
        self._send_json_response(status_code, {
            "status": "error",
            "message": message
        })
    
    def log_message(self, format, *args):
        """重写日志方法，使用统一的logger"""
        logger.info(f"{self.address_string()} - {format % args}")

def create_handler_class(notifier):
    """创建带有notifier实例的处理器类"""
    def handler(*args, **kwargs):
        return EDRAlertHandler(*args, notifier=notifier, **kwargs)
    return handler

def main():
    parser = argparse.ArgumentParser(description="EDR 文件监控告警提醒器")
    parser.add_argument("-p", "--port", type=int, default=8080, 
                       help="监听端口 (默认: 8080)")
    parser.add_argument("-H", "--host", default="0.0.0.0",
                       help="监听地址 (默认: 0.0.0.0)")
    parser.add_argument("--no-sound", action="store_true",
                       help="禁用告警音效")
    parser.add_argument("--test", action="store_true",
                       help="发送测试通知")
    
    args = parser.parse_args()
    
    # 创建通知器
    notifier = EDRNotifier(sound_enabled=not args.no_sound)
    
    # 测试模式
    if args.test:
        print("发送测试通知...")
        notifier.send_notification(
            "🧪 EDR 测试通知", 
            "这是一条测试消息，用于验证通知功能是否正常工作。",
            "info"
        )
        return
    
    # 创建HTTP服务器
    handler_class = create_handler_class(notifier)
    server = HTTPServer((args.host, args.port), handler_class)
    
    print("=" * 60)
    print("🚀 EDR 告警提醒器已启动")
    print("=" * 60)
    print(f"监听地址: http://{args.host}:{args.port}")
    print(f"告警API: http://{args.host}:{args.port}/api/agent/edr-alert")
    print(f"健康检查: http://{args.host}:{args.port}/health")
    print(f"统计信息: http://{args.host}:{args.port}/stats")
    print(f"系统平台: {platform.system()}")
    print(f"通知库: {'plyer' if PLYER_AVAILABLE else '系统原生'}")
    print(f"告警音效: {'启用' if notifier.sound_enabled else '禁用'}")
    print("=" * 60)
    print("按 Ctrl+C 停止服务")
    print()
    
    logger.info(f"EDR告警提醒器启动: {args.host}:{args.port}")
    
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n正在关闭服务器...")
        server.shutdown()
        logger.info("EDR告警提醒器已停止")

if __name__ == "__main__":
    main()