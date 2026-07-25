#include "src/model/LowerZoneState.h"

#include <cmath>

int main()
{
    dc::LowerZoneState state;
    state.setArrangementShare (0.1f);
    if (std::abs (state.getArrangementShare() - 0.25f) > 0.0001f)
        return 1;
    state.setArrangementShare (0.6f);
    if (std::abs (state.getArrangementShare() - 0.6f) > 0.0001f)
        return 2;
    state.setArrangementShare (0.95f);
    if (std::abs (state.getArrangementShare() - 0.85f) > 0.0001f)
        return 3;
    return 0;
}
