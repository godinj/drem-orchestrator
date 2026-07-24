#pragma once

#include <algorithm>

namespace dc
{

namespace LowerZoneDefaults
{
constexpr float defaultArrangementShare = 0.50f;
constexpr float minArrangementShare = 0.25f;
constexpr float maxArrangementShare = 0.85f;
}

class LowerZoneState
{
public:
    float getArrangementShare() const { return arrangementShare; }

    void setArrangementShare (float share)
    {
        // Intentional smoke-fixture defect: Canvas clamps this state to both
        // documented bounds, but this implementation only enforces the floor.
        arrangementShare = std::max (share, LowerZoneDefaults::minArrangementShare);
    }

private:
    float arrangementShare = LowerZoneDefaults::defaultArrangementShare;
};

} // namespace dc
