import { describe, expect, it } from "vitest";

import { approvalReasonText } from "./approval-copy";

describe("approvalReasonText", () => {
	it("翻译已知策略键", () => {
		expect(approvalReasonText("permission_mode_requires_approval")).toContain("权限模式");
		expect(approvalReasonText("plan_mode:model_auto_enter")).toContain("计划模式");
		expect(approvalReasonText("approval_required")).toBe("当前操作需要审批");
		expect(approvalReasonText(" manual approval ")).toBe("当前工具策略要求人工审批");
	});

	it("未知键原样回显并保留原始大小写", () => {
		expect(approvalReasonText("Custom Reason")).toBe("Custom Reason");
	});

	it("空值返回空串", () => {
		expect(approvalReasonText("   ")).toBe("");
	});
});
