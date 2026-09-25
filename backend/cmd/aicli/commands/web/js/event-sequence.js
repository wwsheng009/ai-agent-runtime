// SSE 帧序号守卫（无 DOM 依赖，可在 Node 侧单独验证）。
//
// 背景：服务端 /web/api/events 由**唯一 writer goroutine** 按 FIFO 串行写出，
// 每帧带 `_event.sequence`（web_handlers.go:renderFrame，单连接内单调 +1）。
// 但队列满/流判死时 `enqueue` 是**非阻塞丢帧**（web_handlers.go:496-509），
// 客户端拿不到"缺了哪几帧"的信号；EventSource 自动重连也不带 Last-Event-ID。
//
// 因此客户端必须自己管理帧序号，把三种情况区分开：
//   accept    —— 正常递进（或首帧建立基线）；
//   duplicate —— seq <= 已见（重连重放/重复投递）：丢弃，绝不重复渲染；
//   gap       —— seq 跳号（服务端队列满丢帧）：丢弃该判定但推进基线，
//                 由调用方触发一次权威快照对账（refreshScreen）。
//
// 语义：`reset(seq)` 用于新连接（connected 帧）——每个连接的序号都从 1 重新开始，
// 不 reset 会把重连后的整条流都误判成 duplicate 而全部丢弃。

export function createEventSequenceGuard() {
  var last = 0;

  function normalize(seq) {
    var n = Number(seq);
    if (!isFinite(n) || n <= 0) {
      return 0;
    }
    return Math.floor(n);
  }

  return {
    reset: function (seq) {
      last = normalize(seq);
    },
    lastSeen: function () {
      return last;
    },
    accept: function (seq) {
      var n = normalize(seq);
      if (n === 0) {
        // 无序号帧（历史/自定义端点）：不参与守卫，直接放行。
        return "accept";
      }
      if (last === 0) {
        last = n;
        return "accept";
      }
      if (n === last + 1) {
        last = n;
        return "accept";
      }
      if (n <= last) {
        return "duplicate";
      }
      // 跳号：推进基线到本帧，后续帧继续按 +1 判定。
      last = n;
      return "gap";
    },
  };
}
