package knowledge

import "testing"

// TestExtractJavaCoarseSymbols 钉住 04 §5 Phase 1 交付 1 的 Java 粗符号：
// class / interface / enum / record 与带修饰符的方法；方法体内的局部变量、
// switch 箭头表达式不得误收为符号。
func TestExtractJavaCoarseSymbols(t *testing.T) {
	src := `package demo;

public class Worker extends Base implements Runnable {
    private final Service svc = new Service();

    @Override
    public void run() {
        helper(svc);
        int local = 1;
    }

    public String name() { return "worker"; }

    public int pick(int v) {
        return switch (v) {
            default -> handle(v);
        };
    }
}

interface Handler {
    void handle();
}

enum Color { RED }

record Point(int x, int y) {}
`
	syms, refs := extractNames(t, "src/main/java/demo/Worker.java", "java", src)

	for _, want := range []string{"Worker", "Handler", "Color", "Point", "run", "name", "pick"} {
		if _, ok := syms[want]; !ok {
			t.Fatalf("Java 符号 %q 必须被索引，实际 %v", want, keysOf(syms))
		}
	}
	if syms["Worker"].Kind != SymbolType || !syms["Worker"].IsExported {
		t.Fatalf("Worker = %+v, want public class", syms["Worker"])
	}
	if syms["Handler"].Kind != SymbolInterface {
		t.Fatalf("Handler = %+v, want interface", syms["Handler"])
	}
	if syms["Color"].Kind != SymbolEnum || syms["Point"].Kind != SymbolType {
		t.Fatalf("Color/Point = %+v/%+v", syms["Color"], syms["Point"])
	}
	if syms["run"].Kind != SymbolMethod || !syms["run"].IsExported {
		t.Fatalf("run = %+v, want public method", syms["run"])
	}

	// 局部变量、字段、switch 箭头都不许变成符号。
	for _, unwanted := range []string{"local", "svc", "handle"} {
		if _, ok := syms[unwanted]; ok {
			t.Fatalf("非声明 %q 不得进入轻索引，实际 %v", unwanted, keysOf(syms))
		}
	}
	// 调用点仍是引用候选（只读旁路的输入），不受符号规则影响。
	if refs["helper"] == 0 || refs["handle"] == 0 {
		t.Fatalf("Java 调用点必须产出引用，refs=%v", refs)
	}
}

// TestExtractCppCoarseSymbols 钉住 04 §5 Phase 1 交付 1 的 C/C++ 粗符号：
// struct/enum 与具名函数；return/if 等语句不得被误读成函数定义。
func TestExtractCppCoarseSymbols(t *testing.T) {
	src := `#include <vector>

namespace demo {

struct Job {
    int id;
    void run() { helper(); }
};

enum class State { Ready };

int compute(int a, int b) {
    return helper(a) + b;
}

static int helper(int x) { return x; }

}  // namespace demo
`
	syms, refs := extractNames(t, "svc/native/job.cpp", "cpp", src)

	for _, want := range []string{"Job", "State", "compute", "helper", "run"} {
		if _, ok := syms[want]; !ok {
			t.Fatalf("C++ 符号 %q 必须被索引，实际 %v", want, keysOf(syms))
		}
	}
	if syms["Job"].Kind != SymbolType {
		t.Fatalf("Job = %+v, want struct", syms["Job"])
	}
	if syms["State"].Kind != SymbolEnum {
		t.Fatalf("State = %+v, want enum", syms["State"])
	}
	for _, unwanted := range []string{"id", "namespace", "return"} {
		if _, ok := syms[unwanted]; ok {
			t.Fatalf("非声明 %q 不得进入轻索引，实际 %v", unwanted, keysOf(syms))
		}
	}
	if refs["helper"] == 0 {
		t.Fatalf("C++ 调用点必须产出引用，refs=%v", refs)
	}
}
